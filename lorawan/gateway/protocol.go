// Copyright © 2015 Matthias Benkort <matthias.benkort@gmail.com>
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package protocol

import (
	"errors"
	"time"
)

// Uplink json message format sent by the gateway
// More details can be found in the following Semtech protocol:
// https://github.com/TheThingsNetwork/packet_forwarder/blob/master/PROTOCOL.TXT
type RXPK struct {
	Chan uint      `json:"chan"` // Concentrator "IF" channel used for RX (unsigned integer)
	Codr string    `json:"codr"` // LoRa ECC coding rate identifier
	Data string    `json:"data"` // Base64 encoded RF packet payload, padded
	Datr string    `json:"-"`    // FSK datarate (unsigned in bit per second) || LoRa datarate identifier
	Freq float64   `json:"freq"` // RX Central frequency in MHx (unsigned float, Hz precision)
	Lsnr float64   `json:"lsnr"` // LoRa SNR ratio in dB (signed float, 0.1 dB precision)
	Modu string    `json:"modu"` // Modulation identifier "LORA" or "FSK"
	Rfch uint      `json:"rfch"` // Concentrator "RF chain" used for RX (unsigned integer)
	Rssi int       `json:"rssi"` // RSSI in dBm (signed integer, 1 dB precision)
	Size uint      `json:"size"` // RF packet payload size in bytes (unsigned integer)
	Stat int       `json:"stat"` // CRC status: 1 - OK, -1 = fail, 0 = no CRC
	Time time.Time `json:"-"`    // UTC time of pkt RX, us precision, ISO 8601 'compact' format
	Tmst uint      `json:"tmst"` // Internal timestamp of "RX finished" event (32b unsigned)
}

// Downlink json message format received by the gateway
// Most field are optional
// More details can be found in the following Semtech protocol:
// https://github.com/TheThingsNetwork/packet_forwarder/blob/master/PROTOCOL.TXT
type TXPK struct {
	Codr string    `json:"codr"` // LoRa ECC coding rate identifier
	Data string    `json:"data"` // Base64 encoded RF packet payload, padding optional
	Datr string    `json:"-"`    // LoRa datarate identifier (eg. SF12BW500) || FSK Datarate (unsigned, in bits per second)
	Fdev uint      `json:"fdev"` // FSK frequency deviation (unsigned integer, in Hz)
	Freq float64   `json:"freq"` // TX central frequency in MHz (unsigned float, Hz precision)
	Imme bool      `json:"imme"` // Send packet immediately (will ignore tmst & time)
	Ipol bool      `json:"ipol"` // Lora modulation polarization inversion
	Modu string    `json:"modu"` // Modulation identifier "LORA" or "FSK"
	Ncrc bool      `json:"ncrc"` // If true, disable the CRC of the physical layer (optional)
	Powe uint      `json:"powe"` // TX output power in dBm (unsigned integer, dBm precision)
	Prea uint      `json:"prea"` // RF preamble size (unsigned integer)
	Rfch uint      `json:"rfch"` // Concentrator "RF chain" used for TX (unsigned integer)
	Size uint      `json:"size"` // RF packet payload size in bytes (unsigned integer)
	Time time.Time `json:"-"`    // Send packet at a certain time (GPS synchronization required)
	Tmst uint      `json:"tmst"` // Send packet on a certain timestamp value (will ignore time)
}

// Status json message format sent by the gateway
// More details can be found in the following Semtech protocol:
// https://github.com/TheThingsNetwork/packet_forwarder/blob/master/PROTOCOL.TXT
type Stat struct {
	Ackr float64   `json:"ackr"` // Percentage of upstream datagrams that were acknowledged
	Alti int       `json:"alti"` // GPS altitude of the gateway in meter RX (integer)
	Dwnb uint      `json:"dwnb"` // Number of downlink datagrams received (unsigned integer)
	Lati float64   `json:"lati"` // GPS latitude of the gateway in degree (float, N is +)
	Long float64   `json:"long"` // GPS latitude of the gateway in dgree (float, E is +)
	Rxfw uint      `json:"rxfw"` // Number of radio packets forwarded (unsigned integer)
	Rxnb uint      `json:"rxnb"` // Number of radio packets received (unsigned integer)
	Rxok uint      `json:"rxok"` // Number of radio packets received with a valid PHY CRC
	Time time.Time `json:"-"`    // UTC 'system' time of the gateway, ISO 8601 'expanded' format
	Txnb uint      `json:"txnb"` // Number of packets emitted (unsigned integer)
}

// Packet as seen by the gateway. The payload is optional and could be nil, otherwise, it is a
// Base64 encoding of one of the related message format (RXPK, TXPK or Stat).
// - Version refers to the protocol version, always 1 here
// - Identifier refers to a packet command (PUSH_DATA, PUSH_ACK, PULL_DATA, PULL_RESP, PULL_ACK)
// - Token is a random number generated by the gateway on some request
type Packet struct {
	Version    byte
	Token      []byte
	Identifier byte
    GatewayId  []byte
	Payload    *Payload
}

type Payload struct {
    Raw       []byte  `json:"-"`
    RXPK      *[]RXPK `json:"rxpk"`
    Stat      *Stat   `json:"stat"`
    TXPK      *TXPK   `json:"txpk"`
}

// Available packet commands
const (
	PUSH_DATA byte = iota
	PUSH_ACK
	PULL_DATA
	PULL_RESP
	PULL_ACK
)

// Parse a raw response from a server and turn in into a packet
// Will return an error if the response fields are incorrect
func Parse (raw []byte) (error, *Packet) {
	size := len(raw)

	if size < 3 {
		return errors.New("Invalid raw data format"), nil
	}

	packet := &Packet{
		raw[0],
		raw[1:3],
		raw[3],
        nil,
        nil,
	}

	if packet.Version != 0x1 {
		return errors.New("Unreckognized protocol version"), nil
	}

	if packet.Identifier > PULL_ACK {
		return errors.New("Unreckognized protocol identifier"), nil
	}

    if size >= 12 && packet.Identifier == PULL_DATA {
        packet.GatewayId = raw[4:12]
    }

    var err error
	if size > 4 && packet.Identifier == PUSH_DATA || packet.Identifier == PULL_RESP {
        err, packet.Payload = decodePayload(raw[4:])
	}

	return err, packet
}
