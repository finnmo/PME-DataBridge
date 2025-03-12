package plugins

import (
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/goburrow/modbus"
)

// MbusMapping defines the mapping for one M-Bus device.
type MbusMapping struct {
	DeviceID     string `mapstructure:"deviceID"`
	UnitID       int    `mapstructure:"unitID"`
	BaseRegister int    `mapstructure:"baseRegister"` // Conventional register address (e.g. 40122)
}

// MbusModbusAdapterConfig holds the configuration for the Mbus-Modbus adapter.
type MbusModbusAdapterConfig struct {
	GatewayAddress string        `mapstructure:"gatewayAddress"`
	PollInterval   int           `mapstructure:"pollInterval"` // seconds between polls
	Devices        []MbusMapping `mapstructure:"devices"`      // list of devices to process
}

// MbusModbusAdapter implements the Adapter interface.
type MbusModbusAdapter struct {
	config  MbusModbusAdapterConfig
	handler *modbus.TCPClientHandler
}

// NewMbusModbusAdapter creates a new instance of the Mbus-Modbus adapter.
func NewMbusModbusAdapter(config MbusModbusAdapterConfig) Adapter {
	log.Printf("[Mbus-Modbus Adapter] Creating new adapter with config: %+v", config)
	return &MbusModbusAdapter{config: config}
}

func (m *MbusModbusAdapter) Name() string {
	return "Mbus-Modbus Adapter"
}

// Start polls each configured device at the specified interval.
// For each device, it reads 10 registers (20 bytes) starting at the device's BaseRegister (converted to a zero-based offset using BaseRegister - 40000).
// It then checks the block's type field (register 7, bytes 14-16) to ensure it is a meter value entry (type 0)
// before sending the normalized data on dataCh.
func (m *MbusModbusAdapter) Start(dataCh chan<- Data) error {
	m.handler = modbus.NewTCPClientHandler(m.config.GatewayAddress)
	m.handler.Timeout = 5 * time.Second
	m.handler.SlaveId = 0x01 // typical for the Anybus M-Bus/Modbus gateway

	if err := m.handler.Connect(); err != nil {
		return fmt.Errorf("error connecting to gateway: %w", err)
	}
	client := modbus.NewClient(m.handler)

	go func() {
		defer m.handler.Close()
		ticker := time.NewTicker(time.Duration(m.config.PollInterval) * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			for _, device := range m.config.Devices {
				// Compute the zero-based register offset using 40000 as the base.
				offset := device.BaseRegister - 400000
				if offset < 0 {
					log.Printf("[Mbus-Modbus Adapter] Negative offset computed for device %s: %d", device.DeviceID, offset)
					continue
				}

				// Read 10 registers (20 bytes) for this device.
				results, err := client.ReadHoldingRegisters(uint16(offset), 10)
				if err != nil {
					log.Printf("[Mbus-Modbus Adapter] Error reading registers for device %s: %v", device.DeviceID, err)
					continue
				}
				if len(results) < 20 {
					log.Printf("[Mbus-Modbus Adapter] Insufficient data for device %s (expected 20 bytes, got %d)", device.DeviceID, len(results))
					continue
				}

				// Check the type field in register 7 (bytes 14-16).
				// According to the documentation, the upper byte of register 7 indicates the block type:
				// 1 = Gateway entry, 2 = Meter entry, 0 = Meter value entry.
				typeField := binary.BigEndian.Uint16(results[14:16])
				blockType := (typeField >> 8) & 0xFF
				if blockType != 0 {
					log.Printf("[Mbus-Modbus Adapter] Device %s: Unexpected block type %d at base register %d", device.DeviceID, blockType, device.BaseRegister)
					continue
				}

				// Parse a 64-bit integer from the first 8 bytes of the block.
				value64 := int64(binary.BigEndian.Uint64(results[0:8]))
				normalized := int32(value64) // Adjust scaling if needed.

				log.Printf("[Mbus-Modbus Adapter] Device %s: Read meter value %d from base register %d", device.DeviceID, normalized, device.BaseRegister)
				dataCh <- Data{
					UnitID:         device.UnitID,
					ModbusRegister: device.BaseRegister,
					Liters:         normalized,
				}
			}
		}
	}()
	return nil
}
