package main

import (
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/spf13/viper"
    "PME-DataBridge/modbus"
    "PME-DataBridge/plugins"
)

func loadConfig() error {
    viper.SetConfigName("config")
    viper.SetConfigType("yaml")
    viper.AddConfigPath("./config")
    err := viper.ReadInConfig()
    if err != nil {
        return err
    }
    log.Printf("[CONFIG] Loaded configuration from %s", viper.ConfigFileUsed())
    return nil
}

func main() {
    if err := loadConfig(); err != nil {
        log.Fatalf("[MAIN] Error reading config file: %v", err)
    }

    // Attempt to unmarshal MQTT config
    var mqttConfigs []plugins.MQTTAdapterConfig
    if err := viper.UnmarshalKey("mqtt", &mqttConfigs); err != nil {
        log.Fatalf("[MAIN] Error unmarshalling MQTT configs: %v", err)
    }

    // Attempt to unmarshal MbusModbus config
    var mbusModbusConfig plugins.MbusModbusAdapterConfig
    errMbus := viper.UnmarshalKey("mbusModbus", &mbusModbusConfig)
    if errMbus != nil {
        log.Printf("[MAIN] Notice: could not unmarshal 'mbusModbus' config. Possibly missing or empty. Error: %v", errMbus)
    }

    //Load debug config
    var debugCfg plugins.DebugUIPluginConfig
    if err := viper.UnmarshalKey("debugUI", &debugCfg); err == nil {
        // No error
    }
    
    // Start building the bridging environment
    modbusPort := viper.GetString("modbus.tcp_port")
    log.Printf("[MAIN] Modbus TCP Server will run on port: %s", modbusPort)

    // Create a channel for normalized data from any adapters
    dataCh := make(chan plugins.Data, 100)

    // Create the Modbus server context
    modbusCtx := modbus.NewServerContext()
    log.Printf("[MAIN] Created Modbus server context with %d slave units.", len(modbusCtx.Slaves))


    // Once you've created modbusCtx, you create the debug plugin:
    debugPlugin := plugins.NewDebugUIPlugin(
        plugins.DebugUIPluginConfig{
            Enabled:     debugCfg.Enabled,
            PollSeconds: debugCfg.PollSeconds,
            Units:       debugCfg.Units,
            StartOffset: debugCfg.StartOffset,
            EndOffset:   debugCfg.EndOffset,
        },
        modbusCtx,
    )

    // --- Conditionally start MQTT adapters ---
    if len(mqttConfigs) > 0 {
        log.Printf("[MAIN] Found %d MQTT adapter config(s).", len(mqttConfigs))
        for i, cfg := range mqttConfigs {
            adapter := plugins.NewMQTTAdapter(cfg)
            log.Printf("[MAIN] Starting MQTT adapter instance %d: %s", i+1, adapter.Name())
            go func(a plugins.Adapter) {
                if err := a.Start(dataCh); err != nil {
                    log.Fatalf("[MAIN] Error starting adapter %s: %v", a.Name(), err)
                }
            }(adapter)
        }
    }

    // --- Conditionally start Mbus-Modbus adapter ---
    if mbusModbusConfig.GatewayAddress != "" && len(mbusModbusConfig.Devices) > 0 {
        log.Printf("[MAIN] Found %d M-Bus device(s). Starting Mbus-Modbus adapter.", len(mbusModbusConfig.Devices))
        mbusAdapter := plugins.NewMbusModbusAdapter(mbusModbusConfig)
        log.Printf("[MAIN] Starting Mbus-Modbus adapter: %s", mbusAdapter.Name())
        go func() {
            if err := mbusAdapter.Start(dataCh); err != nil {
                log.Fatalf("[MAIN] Error starting Mbus-Modbus adapter: %v", err)
            }
        }()
    }

    // Start the debug plugin
    go func() {
        if err := debugPlugin.Start(nil); err != nil {
            log.Printf("[MAIN] Error starting DebugUI plugin: %v", err)
        }
    }()
    // Process incoming normalized data
    go func() {
        for d := range dataCh {
            log.Printf("[DATA] Received normalized data: %+v", d)
            // Compute offset = modbusRegister - 400001 (matching Python approach)
            registerOffset := d.ModbusRegister - 400001
            if registerOffset < 0 {
                log.Printf("[DATA] Computed negative register offset: %d for data %v", registerOffset, d)
                continue
            }
            modbusCtx.UpdateRegister(d.UnitID, registerOffset, d.Liters)
        }
    }()

    // Start the Modbus TCP server
    go func() {
        addr := "0.0.0.0:" + modbusPort
        log.Printf("[MAIN] Starting Modbus TCP server on %s", addr)
        if err := modbus.StartTCPServer(modbusCtx, addr); err != nil {
            log.Fatalf("[MAIN] Error starting Modbus TCP server: %v", err)
        }
    }()

    // Wait for a termination signal
    sigs := make(chan os.Signal, 1)
    signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
    <-sigs
    log.Println("[MAIN] Shutting down...")
    time.Sleep(1 * time.Second)
}

