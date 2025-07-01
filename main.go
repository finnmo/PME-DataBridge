package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"PME-DataBridge/modbus"
	"PME-DataBridge/plugins"

	"github.com/spf13/viper"
)

// Global variables for monitoring
var (
	lastDataTime time.Time
	dataCount    int
	dataMutex    sync.RWMutex
	systemHealth struct {
		mqttConnected   bool
		modbusListening bool
		lastHealthCheck time.Time
		errorCount      int
	}
	healthMutex sync.RWMutex
)

func validateConfig() error {
	if !viper.IsSet("modbus.tcp_port") {
		return fmt.Errorf("modbus.tcp_port is required")
	}
	if !viper.IsSet("mqtt") {
		return fmt.Errorf("mqtt configuration is required")
	}
	return nil
}

func loadConfig() error {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./config")
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("error reading config file: %w", err)
	}
	log.Printf("[CONFIG] Loaded configuration from %s", viper.ConfigFileUsed())

	if err := validateConfig(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}

func main() {
	if err := loadConfig(); err != nil {
		log.Fatalf("[MAIN] %v", err)
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

	// Load debug config
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

	// Load saved register values
	if err := modbusCtx.LoadSavedValues(); err != nil {
		log.Printf("[MAIN] Warning: Could not load saved values: %v", err)
	}

	// Create debug plugin
	debugPlugin := plugins.NewDebugUIPlugin(debugCfg, modbusCtx)

	// Start all adapters
	var adapters []plugins.Adapter

	// Start MQTT adapters
	for i, cfg := range mqttConfigs {
		adapter := plugins.NewMQTTAdapter(cfg)
		adapters = append(adapters, adapter)
		log.Printf("[MAIN] Starting MQTT adapter instance %d: %s", i+1, adapter.Name())
		go func(a plugins.Adapter) {
			if err := a.Start(dataCh); err != nil {
				log.Printf("[MAIN] Error starting adapter %s: %v", a.Name(), err)
			}
		}(adapter)
	}

	// Start MQTT connection monitoring
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			log.Printf("[MAIN] MQTT connection status check - %d adapters running", len(adapters))
		}
	}()

	// Start Mbus-Modbus adapter if configured
	if mbusModbusConfig.GatewayAddress != "" && len(mbusModbusConfig.Devices) > 0 {
		mbusAdapter := plugins.NewMbusModbusAdapter(mbusModbusConfig)
		adapters = append(adapters, mbusAdapter)
		log.Printf("[MAIN] Starting Mbus-Modbus adapter: %s", mbusAdapter.Name())
		go func() {
			if err := mbusAdapter.Start(dataCh); err != nil {
				log.Printf("[MAIN] Error starting Mbus-Modbus adapter: %v", err)
			}
		}()
	}

	// Start debug plugin
	go func() {
		if err := debugPlugin.Start(nil); err != nil {
			log.Printf("[MAIN] Error starting DebugUI plugin: %v", err)
		}
	}()

	// Process incoming normalized data
	go func() {
		for d := range dataCh {
			dataMutex.Lock()
			lastDataTime = time.Now()
			dataCount++
			dataMutex.Unlock()

			log.Printf("[DATA] Received normalized data: %+v", d)
			registerOffset := d.ModbusRegister - 400001
			if registerOffset < 0 {
				log.Printf("[DATA] Computed negative register offset: %d for data %v", registerOffset, d)
				continue
			}
			modbusCtx.UpdateRegister(d.UnitID, registerOffset, d.Liters)
		}
	}()

	// System health monitoring
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			healthMutex.Lock()
			systemHealth.lastHealthCheck = time.Now()

			// Check data flow
			dataMutex.RLock()
			timeSinceLastData := time.Since(lastDataTime)
			dataMutex.RUnlock()

			// Check MQTT connections
			mqttConnected := true
			for i, adapter := range adapters {
				if mqttAdapter, ok := adapter.(*plugins.MQTTAdapter); ok {
					if !mqttAdapter.IsConnected() {
						mqttConnected = false
						log.Printf("[HEALTH] MQTT adapter %d is disconnected", i+1)
					}
				}
			}
			systemHealth.mqttConnected = mqttConnected

			// Log health status
			if timeSinceLastData > 15*time.Minute {
				log.Printf("[HEALTH ALERT] System health check - No data for %v, MQTT: %v",
					timeSinceLastData, mqttConnected)
				systemHealth.errorCount++

				// Trigger recovery actions
				if systemHealth.errorCount > 3 {
					log.Printf("[HEALTH ALERT] Multiple errors detected, triggering recovery...")
					// Restart MQTT adapters
					for i, adapter := range adapters {
						if mqttAdapter, ok := adapter.(*plugins.MQTTAdapter); ok {
							log.Printf("[HEALTH] Restarting MQTT adapter %d", i+1)
							if err := mqttAdapter.Reconnect(); err != nil {
								log.Printf("[HEALTH] Failed to restart MQTT adapter %d: %v", i+1, err)
							}
						}
					}
					systemHealth.errorCount = 0
				}
			} else {
				log.Printf("[HEALTH] System healthy - Data flow: %v ago, MQTT: %v, Errors: %d",
					timeSinceLastData, mqttConnected, systemHealth.errorCount)
				if systemHealth.errorCount > 0 {
					systemHealth.errorCount--
				}
			}
			healthMutex.Unlock()
		}
	}()

	// Start the Modbus TCP server
	go func() {
		addr := "0.0.0.0:" + modbusPort
		log.Printf("[MAIN] Starting Modbus TCP server on %s", addr)
		if err := modbus.StartTCPServer(modbusCtx, addr); err != nil {
			log.Printf("[MAIN] Error starting Modbus TCP server: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Println("[MAIN] Shutdown signal received, initiating graceful shutdown...")

	// Start graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Close data channel
	close(dataCh)

	// Flush any remaining buffered messages
	log.Println("[MAIN] Flushing buffered messages...")
	for _, adapter := range adapters {
		if mqttAdapter, ok := adapter.(*plugins.MQTTAdapter); ok {
			bufferedMessages := mqttAdapter.GetBufferedMessages()
			if len(bufferedMessages) > 0 {
				log.Printf("[MAIN] Flushing %d buffered messages from MQTT adapter", len(bufferedMessages))
				// Process buffered messages
				for _, data := range bufferedMessages {
					registerOffset := data.ModbusRegister - 400001
					if registerOffset >= 0 {
						modbusCtx.UpdateRegister(data.UnitID, registerOffset, data.Liters)
					}
				}
			}
		}
	}

	// Disconnect MQTT clients
	log.Println("[MAIN] Disconnecting MQTT clients...")
	for _, adapter := range adapters {
		if mqttAdapter, ok := adapter.(*plugins.MQTTAdapter); ok {
			mqttAdapter.Disconnect()
		}
	}

	// Wait for shutdown timeout or completion
	select {
	case <-shutdownCtx.Done():
		log.Println("[MAIN] Shutdown timed out")
	default:
		log.Println("[MAIN] Shutdown completed successfully")
	}
}
