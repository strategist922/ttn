// Copyright © 2017 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"time"

	pb_broker "github.com/TheThingsNetwork/api/broker"
	"github.com/TheThingsNetwork/api/broker/brokerclient"
	pb "github.com/TheThingsNetwork/api/handler"
	"github.com/TheThingsNetwork/api/monitor/monitorclient"
	pb_lorawan "github.com/TheThingsNetwork/api/protocol/lorawan"
	"github.com/TheThingsNetwork/go-utils/grpc/auth"
	"github.com/TheThingsNetwork/ttn/amqp"
	"github.com/TheThingsNetwork/ttn/core/component"
	"github.com/TheThingsNetwork/ttn/core/handler/application"
	"github.com/TheThingsNetwork/ttn/core/handler/device"
	"github.com/TheThingsNetwork/ttn/core/types"
	"github.com/TheThingsNetwork/ttn/mqtt"
	"google.golang.org/grpc"
	"gopkg.in/redis.v5"
)

// Handler component
type Handler interface {
	component.Interface
	component.ManagementInterface

	WithMQTT(username, password string, brokers ...string) Handler
	WithAMQP(username, password, host, exchange string) Handler
	WithDeviceAttributes(attribute ...string) Handler

	HandleUplink(uplink *pb_broker.DeduplicatedUplinkMessage) error
	HandleActivationChallenge(challenge *pb_broker.ActivationChallengeRequest) (*pb_broker.ActivationChallengeResponse, error)
	HandleActivation(activation *pb_broker.DeduplicatedDeviceActivationRequest) (*pb.DeviceActivationResponse, error)
	EnqueueDownlink(appDownlink *types.DownlinkMessage) error
}

// NewRedisHandler creates a new Redis-backed Handler
func NewRedisHandler(client *redis.Client, ttnBrokerID string) Handler {
	return &handler{
		devices:      device.NewRedisDeviceStore(client, "handler"),
		applications: application.NewRedisApplicationStore(client, "handler"),
		ttnBrokerID:  ttnBrokerID,
		qUp:          make(chan *types.UplinkMessage),
		qEvent:       make(chan *types.DeviceEvent),
	}
}

type handler struct {
	*component.Component

	devices      device.Store
	applications application.Store

	ttnBrokerID      string
	ttnBrokerConn    *grpc.ClientConn
	ttnBroker        pb_broker.BrokerClient
	ttnBrokerManager pb_broker.BrokerManagerClient
	ttnDeviceManager pb_lorawan.DeviceManagerClient

	downlink chan *pb_broker.DownlinkMessage

	mqttClient   mqtt.Client
	mqttUsername string
	mqttPassword string
	mqttBrokers  []string
	mqttEnabled  bool
	mqttUp       chan *types.UplinkMessage
	mqttEvent    chan *types.DeviceEvent

	amqpClient   amqp.Client
	amqpUsername string
	amqpPassword string
	amqpHost     string
	amqpExchange string
	amqpEnabled  bool
	amqpUp       chan *types.UplinkMessage
	amqpEvent    chan *types.DeviceEvent

	qUp    chan *types.UplinkMessage
	qEvent chan *types.DeviceEvent

	status        *status
	monitorStream monitorclient.Stream
}

var (
	// AMQPDownlinkQueue is the AMQP queue to use for downlink
	AMQPDownlinkQueue = "ttn-handler-downlink"
)

func (h *handler) WithMQTT(username, password string, brokers ...string) Handler {
	h.mqttUsername = username
	h.mqttPassword = password
	h.mqttBrokers = brokers
	h.mqttEnabled = true
	return h
}

func (h *handler) WithAMQP(username, password, host, exchange string) Handler {
	h.amqpUsername = username
	h.amqpPassword = password
	h.amqpHost = host
	h.amqpExchange = exchange
	h.amqpEnabled = true
	return h
}

func (h *handler) WithDeviceAttributes(a ...string) Handler {
	h.devices.AddBuiltinAttribute(a...)
	return h
}

func (h *handler) Init(c *component.Component) error {
	h.Component = c
	h.InitStatus()
	err := h.Component.UpdateTokenKey()
	if err != nil {
		return err
	}

	err = h.Announce()
	if err != nil {
		return err
	}

	if h.mqttEnabled {
		var brokers []string
		for _, broker := range h.mqttBrokers {
			brokers = append(brokers, fmt.Sprintf("tcp://%s", broker))
		}
		err = h.HandleMQTT(h.mqttUsername, h.mqttPassword, brokers...)
		if err != nil {
			return err
		}
	}

	if h.amqpEnabled {
		err = h.HandleAMQP(h.amqpUsername, h.amqpPassword, h.amqpHost, h.amqpExchange, AMQPDownlinkQueue)
		if err != nil {
			return err
		}
	}

	go func() {
		for {
			select {
			case up := <-h.qUp:
				if h.mqttEnabled {
					h.mqttUp <- up
				}
				if h.amqpEnabled {
					h.amqpUp <- up
				}
			case event := <-h.qEvent:
				if h.mqttEnabled {
					h.mqttEvent <- event
				}
				if h.amqpEnabled {
					h.amqpEvent <- event
				}
			}
		}
	}()

	err = h.associateBroker()
	if err != nil {
		return err
	}

	h.Component.SetStatus(component.StatusHealthy)
	if h.Component.Monitor != nil {
		h.monitorStream = h.Component.Monitor.HandlerClient(h.Context, grpc.PerRPCCredentials(auth.WithStaticToken(h.AccessToken)))
		go func() {
			for range time.Tick(h.Component.Config.StatusInterval) {
				h.monitorStream.Send(h.GetStatus())
			}
		}()
	}

	return nil
}

func (h *handler) Shutdown() {
	if h.mqttEnabled {
		h.mqttClient.Disconnect()
	}
	if h.amqpEnabled {
		h.amqpClient.Disconnect()
	}
}

func (h *handler) associateBroker() error {
	broker, err := h.Discover("broker", h.ttnBrokerID)
	if err != nil {
		return err
	}
	conn, err := broker.Dial(h.Pool)
	if err != nil {
		return err
	}
	h.ttnBrokerConn = conn
	h.ttnBroker = pb_broker.NewBrokerClient(conn)
	h.ttnBrokerManager = pb_broker.NewBrokerManagerClient(conn)
	h.ttnDeviceManager = pb_lorawan.NewDeviceManagerClient(conn)

	h.downlink = make(chan *pb_broker.DownlinkMessage)

	config := brokerclient.DefaultClientConfig
	config.BackgroundContext = h.Component.Context
	cli := brokerclient.NewClient(config)
	cli.AddServer(h.ttnBrokerID, h.ttnBrokerConn)
	association := cli.NewHandlerStreams(h.Identity.ID, "")

	go func() {
		for {
			select {
			case message := <-h.downlink:
				association.Downlink(message)
			case message, ok := <-association.Uplink():
				if ok {
					go h.HandleUplink(message)
				}
			}
		}
	}()

	return nil
}
