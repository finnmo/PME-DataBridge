package plugins

import (
    "encoding/binary"
    "fmt"
    "log"
    "time"

    "github.com/goburrow/modbus"
)

// Adjusted config with a TotalBlocks param for illustration.
type MbusModbusAdapterConfig struct {
    GatewayAddress string        `mapstructure:"gatewayAddress"`
    PollInterval   int           `mapstructure:"pollInterval"`
    TotalBlocks    int           `mapstructure:"totalBlocks"`
}

type MbusModbusAdapter struct {
    config  MbusModbusAdapterConfig
    handler *modbus.TCPClientHandler
}

func NewMbusModbusAdapter(config MbusModbusAdapterConfig) Adapter {
    log.Printf("[Mbus-Modbus Adapter] Creating new adapter with config: %+v", config)
    return &MbusModbusAdapter{ config: config }
}

func (m *MbusModbusAdapter) Name() string {
    return "Mbus-Modbus Adapter"
}

// Start reads all blocks at once, then parses them in 10-register increments.
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

        // Each block is 10 registers => read them all at once.
        numRegisters := uint16(m.config.TotalBlocks * 10)

        for range ticker.C {
            if numRegisters == 0 {
                log.Println("[Mbus-Modbus Adapter] No blocks configured, skipping read.")
                continue
            }

            // 1) Read the entire range in one shot.
            results, err := client.ReadHoldingRegisters(0, numRegisters)
            if err != nil {
                log.Printf("[Mbus-Modbus Adapter] Error reading %d registers: %v", numRegisters, err)
                continue
            }
            if len(results) < int(numRegisters)*2 {
                log.Printf("[Mbus-Modbus Adapter] Insufficient data (expected %d bytes, got %d).",
                    int(numRegisters)*2, len(results))
                continue
            }

            // 2) Parse each 10-register (20-byte) block
            blockSize := 10
            bytesPerBlock := blockSize * 2 // 20 bytes
            for blockIndex := 0; blockIndex < m.config.TotalBlocks; blockIndex++ {
                startByte := blockIndex * bytesPerBlock
                block := results[startByte : startByte+bytesPerBlock]

                // The doc says register 7 (the 8th register in the block) contains the "type field" in its upper byte.
                // That means block[14..16) => big-endian 16 bits. The high byte is the "type".
                // For example:
                //   1 => gateway entry
                //   2 => meter entry
                //   0 => meter value entry
                typeField := binary.BigEndian.Uint16(block[14:16])
                blockType := (typeField >> 8) & 0xFF // upper byte

                switch blockType {
                case 1:
                    // Gateway entry
                    log.Printf("[Mbus-Modbus Adapter] Block %d => Gateway entry", blockIndex)
                    // parse gateway fields from block[0..19] as needed
                    // e.g. block[0..4] => serial number, block[4..8] => timestamp, etc.

                case 2:
                    // Meter entry
                    log.Printf("[Mbus-Modbus Adapter] Block %d => Meter entry", blockIndex)
                    // parse meter serial, manufacturer ID, etc.
                    // you might create a "meter" struct, store it, or send data to dataCh.

                case 0:
                    // Meter value entry
                    log.Printf("[Mbus-Modbus Adapter] Block %d => Meter value entry", blockIndex)
                    // parse 64-bit integer from block[0..8], float from block[8..12], etc.
                    // For example, read a 64-bit integer from the first 8 bytes:
                    value64 := int64(binary.BigEndian.Uint64(block[0:8]))
                    // or read the float from block[8..12], scale factor at block[12..14], etc.
                    // Then send normalized data if you want:
                    dataCh <- Data{
                        UnitID:         1, // or figure out which meter this belongs to
                        ModbusRegister: int(40001 + blockIndex*10),
                        Liters:         int32(value64), // or do your own scaling
                    }

                default:
                    log.Printf("[Mbus-Modbus Adapter] Block %d => Unknown type: %d", blockIndex, blockType)
                }
            }
        }
    }()
    return nil
}
