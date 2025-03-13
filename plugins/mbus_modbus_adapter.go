package plugins

import (
    "encoding/binary"
    "fmt"
    "log"
    "time"

    "github.com/goburrow/modbus"
)

type MbusMapping struct {
    DeviceID       string `mapstructure:"deviceID"`
    UnitID         int    `mapstructure:"unitID"`
    SlaveRegister  int    `mapstructure:"slaveRegister"`  // read from gateway
    ServerRegister int    `mapstructure:"serverRegister"` // write to local server
}

// MbusModbusAdapterConfig now no longer has a "GlobalRegister" field
type MbusModbusAdapterConfig struct {
    GatewayAddress string        `mapstructure:"gatewayAddress"`
    PollInterval   int           `mapstructure:"pollInterval"`
    Devices        []MbusMapping `mapstructure:"devices"`
}

// MbusModbusAdapter ...
type MbusModbusAdapter struct {
    config  MbusModbusAdapterConfig
    handler *modbus.TCPClientHandler
}

func NewMbusModbusAdapter(config MbusModbusAdapterConfig) Adapter {
    return &MbusModbusAdapter{config: config}
}

func (m *MbusModbusAdapter) Name() string {
    return "Mbus-Modbus Adapter"
}

// Start reads from (SlaveRegister - 400000) + 4 as a single 16-bit register, then sends it to
// "serverRegister" in your local server, consistent with the new device-based config.
func (m *MbusModbusAdapter) Start(dataCh chan<- Data) error {
    m.handler = modbus.NewTCPClientHandler(m.config.GatewayAddress)
    m.handler.Timeout = 5 * time.Second
    m.handler.SlaveId = 0x01

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
                log.Printf("Device %s -> slaveReg=%d, serverReg=%d", 
                           device.DeviceID, device.SlaveRegister, device.ServerRegister)

                // Example: read 1 register at offset = (SlaveRegister - 400000) + 4
                offset := (device.SlaveRegister - 400000) + 4
                if offset < 0 {
                    log.Printf("[Mbus-Modbus] Negative offset for device %s: %d", device.DeviceID, offset)
                    continue
                }

                results, err := client.ReadHoldingRegisters(uint16(offset), 1)
                if err != nil {
                    log.Printf("[Mbus-Modbus] Error reading device %s: %v", device.DeviceID, err)
                    continue
                }
                if len(results) < 2 {
                    log.Printf("[Mbus-Modbus] Insufficient data for device %s (need 2 bytes, got %d)", device.DeviceID, len(results))
                    continue
                }

                // Parse big-endian signed 16-bit
                raw16 := int16(binary.BigEndian.Uint16(results[0:2]))
                value32 := int32(raw16)

                log.Printf("[Mbus-Modbus] Device %s: read value %d from offset %d => writing to serverReg %d",
                           device.DeviceID, value32, offset, device.ServerRegister)

                // Send data to local server using serverRegister
                dataCh <- Data{
                    UnitID:         device.UnitID,
                    ModbusRegister: device.ServerRegister,
                    Liters:         value32,
                }

                time.Sleep(200 * time.Millisecond) // Avoid flooding gateway
            }
        }
    }()
    return nil
}
