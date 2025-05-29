package plugins

import (
    "log"
    "time"

    "PME-DataBridge/modbus"
)

// DebugUIPluginConfig holds optional settings for the debug output.
type DebugUIPluginConfig struct {
    Enabled     bool   `mapstructure:"enabled"`
    PollSeconds int    `mapstructure:"pollSeconds"`
    // Possibly a list of units/offset ranges to display
    Units       []int  `mapstructure:"units"`
    StartOffset int    `mapstructure:"startOffset"`
    EndOffset   int    `mapstructure:"endOffset"`
}

// DebugUIPlugin is a plugin that reads from the local modbus server context
// and displays register values for debugging.
type DebugUIPlugin struct {
    config DebugUIPluginConfig
    server *modbus.ServerContext // reference to your local server
}

// NewDebugUIPlugin creates the plugin with config & reference to local server.
func NewDebugUIPlugin(cfg DebugUIPluginConfig, server *modbus.ServerContext) Adapter {
    return &DebugUIPlugin{
        config: cfg,
        server: server,
    }
}

func (d *DebugUIPlugin) Name() string {
    return "Debug UI Plugin"
}

func (d *DebugUIPlugin) Start(dataCh chan<- Data) error {
    if !d.config.Enabled {
        log.Println("[DebugUI] Plugin disabled; not starting.")
        return nil
    }
    if d.config.PollSeconds < 1 {
        d.config.PollSeconds = 5
    }

    // Start a goroutine that periodically logs out register data
    go func() {
        ticker := time.NewTicker(time.Duration(d.config.PollSeconds) * time.Second)
        defer ticker.Stop()

        for range ticker.C {
            d.displayRegisters()
        }
    }()
    return nil
}

// displayRegisters enumerates the requested units & offsets and logs them.
func (d *DebugUIPlugin) displayRegisters() {
    log.Println("[DebugUI] ---- Current Register Values ----")
    for _, unit := range d.config.Units {
        slave, ok := d.server.Slaves[unit]
        if !ok {
            log.Printf("[DebugUI] Unit %d not found.", unit)
            continue
        }
        slave.Mu.RLock()
        for offset := d.config.StartOffset; offset <= d.config.EndOffset-1; offset += 2 {
            // read two 16-bit registers: [offset], [offset+1]
            if offset+1 < len(slave.HR) {
                r1 := slave.HR[offset]
                r2 := slave.HR[offset+1]

                // Merge them as big-endian 32-bit
                high := uint32(r1)
                low :=  uint32(r2)
                raw32 := (high << 16) | low
                val := int32(raw32) // interpret top bit as sign

                log.Printf("[DebugUI] Unit=%d offset=%d => 0x%04X%04X (dec %d)", unit, offset, r1, r2, val)
            }
        }
        slave.Mu.RUnlock()
    }
    log.Println("[DebugUI] ---------------------------------")
}
