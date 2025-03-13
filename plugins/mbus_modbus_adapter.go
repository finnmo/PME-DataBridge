package plugins

import (
    "encoding/binary"
    "fmt"
    "log"
    "time"

    "github.com/goburrow/modbus"
)

// MbusMapping defines your per-device config.
type MbusMapping struct {
    DeviceID       string `mapstructure:"deviceID"`
    UnitID         int    `mapstructure:"unitID"`
    SlaveRegister  int    `mapstructure:"slaveRegister"`  // The block start address from the gateway, e.g. 400010
    ServerRegister int    `mapstructure:"serverRegister"` // The local server's conventional register, e.g. 403021
}

type MbusModbusAdapterConfig struct {
    GatewayAddress string        `mapstructure:"gatewayAddress"`
    PollInterval   int           `mapstructure:"pollInterval"`
    Devices        []MbusMapping `mapstructure:"devices"`
}

// MbusModbusAdapter polls each device, reading 2 registers from (SlaveRegister - 400000) + offset
// in big-endian, 2's complement format, then writes the final 32-bit SINT to the bridging channel.
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

// Start connects to the Anybus gateway and polls each device at PollInterval.
// If, for example, the doc says your 32-bit SINT is at offsets +3,+4 relative to SlaveRegister,
// you can adjust that offset below as needed.
func (m *MbusModbusAdapter) Start(dataCh chan<- Data) error {
    m.handler = modbus.NewTCPClientHandler(m.config.GatewayAddress)
    m.handler.Timeout = 5 * time.Second
    m.handler.SlaveId = 0x01 // typical for an Anybus gateway

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
                // Example offset: we read the doc says the 32-bit data is found at (block + 3..4)
                // If the block start is device.SlaveRegister, we do:
                readOffset := (device.SlaveRegister - 400001) + 3
                if readOffset < 0 {
                    log.Printf("[Mbus-Modbus] Negative offset for device %s: %d", device.DeviceID, readOffset)
                    continue
                }

                // Read 2 consecutive 16-bit registers => 4 bytes
                results, err := client.ReadHoldingRegisters(uint16(readOffset), 2)
                if err != nil {
                    log.Printf("[Mbus-Modbus] Error reading device %s: %v", device.DeviceID, err)
                    continue
                }
                if len(results) < 4 {
                    log.Printf("[Mbus-Modbus] Not enough bytes (need 4), got %d", len(results))
                    continue
                }

                // Combine into a big-endian 32-bit. e.g. 0x0001 + 0x0FEF => 0x00010FEF => decimal 69095
                // Then interpret it as signed => int32.
                raw32 := binary.BigEndian.Uint32(results[:4])
                value32 := int32(raw32)

                // If the top bit of raw32 is set, value32 will be negative. Otherwise positive.
                // e.g. 0x0001_0FEF => 0x00010FEF =>  0x00010FEF decimal: 67567 (?) or 0x0005_F538 => ...
                log.Printf("[Mbus-Modbus] Device %s => 0x%08X (dec %d) at offset [%d,%d] => localReg %d",
                    device.DeviceID, raw32, value32, readOffset, readOffset+1, device.ServerRegister)

                // We pass the final 32-bit SINT into bridging code. The bridging code does:
                // offset = serverRegister - 400001 => big-endian store in local server => consistent with MQTT
                dataCh <- Data{
                    UnitID:         device.UnitID,
                    ModbusRegister: device.ServerRegister,
                    Liters:         value32,
                }

                time.Sleep(200 * time.Millisecond)
            }
        }
    }()
    return nil
}
