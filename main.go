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

// loadConfig loads the configuration and logs what was loaded.
func loadConfig() error {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./config")
	err := viper.ReadInConfig()
	if err != nil {
		return err
	}
	log.Printf("[CONFIG] Loaded configuration from %s", viper.ConfigFileUsed())
	log.Printf("[CONFIG] MQTT Config: %+v", viper.Get("mqtt"))
	log.Printf("[CONFIG] MbusModbus Config: %+v", viper.Get("mbusModbus"))
	log.Printf("[CONFIG] Modbus Config: %+v", viper.Get("modbus"))
	return nil
}

func main() {
	// Load configuration.
	if err := loadConfig(); err != nil {
		log.Fatalf("[MAIN] Error reading config file: %v", err)
	}

	// Load multiple MQTT adapter configurations.
	var mqttConfigs []plugins.MQTTAdapterConfig
	if err := viper.UnmarshalKey("mqtt", &mqttConfigs); err != nil {
		log.Fatalf("[MAIN] Error unmarshalling MQTT configs: %v", err)
	}
	log.Printf("[MAIN] Found %d MQTT adapter configurations.", len(mqttConfigs))

	// Load the MbusModbus adapter configuration.
	var mbusModbusConfig plugins.MbusModbusAdapterConfig
	if err := viper.UnmarshalKey("mbusModbus", &mbusModbusConfig); err != nil {
		log.Fatalf("[MAIN] Error unmarshalling mbusModbus config: %v", err)
	}
	log.Printf("[MAIN] MbusModbus Adapter Config: %+v", mbusModbusConfig)

	// Get Modbus configuration.
	modbusPort := viper.GetString("modbus.tcp_port")
	log.Printf("[MAIN] Modbus TCP Server will run on port: %s", modbusPort)

	// Create a channel for normalized data.
	dataCh := make(chan plugins.Data, 100)

	// Create the Modbus server context.
	modbusCtx := modbus.NewServerContext()
	log.Printf("[MAIN] Created Modbus server context with %d slave units.", len(modbusCtx.Slaves))

	// Start each MQTT adapter instance.
	for i, cfg := range mqttConfigs {
		adapter := plugins.NewMQTTAdapter(cfg)
		log.Printf("[MAIN] Starting MQTT adapter instance %d: %s", i+1, adapter.Name())
		go func(a plugins.Adapter) {
			if err := a.Start(dataCh); err != nil {
				log.Fatalf("[MAIN] Error starting adapter %s: %v", a.Name(), err)
			}
		}(adapter)
	}

	// Start the MbusModbus adapter.
	mbusAdapter := plugins.NewMbusModbusAdapter(mbusModbusConfig)
	log.Printf("[MAIN] Starting Mbus-Modbus adapter: %s", mbusAdapter.Name())
	go func() {
		if err := mbusAdapter.Start(dataCh); err != nil {
			log.Fatalf("[MAIN] Error starting Mbus-Modbus adapter: %v", err)
		}
	}()

	// Process incoming normalized data.
	go func() {
		for d := range dataCh {
			log.Printf("[DATA] Received normalized data: %+v", d)
			// Compute zero-based register offset.
			registerOffset := d.ModbusRegister - 400001
			if registerOffset < 0 {
				log.Printf("[DATA] Computed negative register offset: %d", registerOffset)
				continue
			}
			modbusCtx.UpdateRegister(d.UnitID, registerOffset, d.Liters)
		}
	}()

	// Start the Modbus TCP server.
	go func() {
		addr := "0.0.0.0:" + modbusPort
		log.Printf("[MAIN] Starting Modbus TCP server on %s", addr)
		if err := modbus.StartTCPServer(modbusCtx, addr); err != nil {
			log.Fatalf("[MAIN] Error starting Modbus TCP server: %v", err)
		}
	}()

	// Wait for a termination signal.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Println("[MAIN] Shutting down...")
	time.Sleep(1 * time.Second)
}
