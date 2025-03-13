package plugins

import (
    "crypto/tls"
    "crypto/x509"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "strconv"

    mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MQTTAdapterConfig holds configuration settings for the MQTT adapter.
type MQTTAdapterConfig struct {
    Broker   string `mapstructure:"broker"`
    Port     int    `mapstructure:"port"`
    Topic    string `mapstructure:"topic"`
    CA       string `mapstructure:"ca"`
    Cert     string `mapstructure:"cert"`
    Key      string `mapstructure:"key"`
    Username string `mapstructure:"username"`
    Password string `mapstructure:"password"`
}

// MQTTAdapter implements the Adapter interface.
type MQTTAdapter struct {
    config MQTTAdapterConfig
    client mqtt.Client
}

// NewMQTTAdapter creates a new MQTTAdapter given its configuration.
func NewMQTTAdapter(config MQTTAdapterConfig) Adapter {
    return &MQTTAdapter{
        config: config,
    }
}

func (m *MQTTAdapter) Name() string {
    return "MQTT Adapter"
}

// Start connects to the MQTT broker, subscribes to the topic, and processes messages.
func (m *MQTTAdapter) Start(dataCh chan<- Data) error {
    tlsConfig, err := newTLSConfig(m.config.CA, m.config.Cert, m.config.Key)
    if err != nil {
        return err
    }

    opts := mqtt.NewClientOptions()
    brokerURL := fmt.Sprintf("%s:%d", m.config.Broker, m.config.Port)
    opts.AddBroker(brokerURL)
    opts.SetClientID("PME-DataBridge-mqtt")
    opts.SetTLSConfig(tlsConfig)

    if m.config.Username != "" {
        opts.SetUsername(m.config.Username)
    }
    if m.config.Password != "" {
        opts.SetPassword(m.config.Password)
    }

    // This publish handler is called for each incoming message on the subscribed topic(s).
    opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
        // Minimal logging – just say we got something from the topic
        // More details will be in the final bridging logs or a consolidated message
        // We do a quick parse to avoid duplication in bridging logs.
        var payload map[string]interface{}
        if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
            log.Printf("[MQTT Adapter] JSON parse error: %v", err)
            return
        }

        // Attempt to parse the relevant fields:
        message, _ := payload["message"].(map[string]interface{})
        body, _ := message["body"].(map[string]interface{})
        transformed, _ := body["transformedPayload"].(map[string]interface{})
        litersRaw, _ := transformed["liters"]
        attributes, _ := body["attributes"].(map[string]interface{})

        var liters int32
        switch v := litersRaw.(type) {
        case float64:
            liters = int32(v)
        case string:
            f, err := strconv.ParseFloat(v, 64)
            if err != nil {
                log.Printf("[MQTT Adapter] Cannot parse liters from string: %v", err)
                return
            }
            liters = int32(f)
        default:
            // We'll skip if we can't parse
            return
        }
        unitID, errU := parseInt(attributes["unitID"])
        modbusRegister, errR := parseInt(attributes["modbusRegister"])
        if errU != nil || errR != nil {
            log.Printf("[MQTT Adapter] Missing or invalid unitID/modbusRegister in message.")
            return
        }

        // Combine all relevant info into a single log line
        log.Printf("[MQTT->DataCh] liters=%d, unitID=%d, reg=%d", liters, unitID, modbusRegister)

        // Pass normalized data to bridging
        dataCh <- Data{
            UnitID:         unitID,
            ModbusRegister: modbusRegister,
            Liters:         liters,
        }
    })

    opts.OnConnect = func(client mqtt.Client) {
        log.Printf("[MQTT Adapter] Connected to %s", brokerURL)
    }
    opts.OnConnectionLost = func(client mqtt.Client, err error) {
        log.Printf("[MQTT Adapter] Connection lost: %v", err)
    }

    m.client = mqtt.NewClient(opts)
    if token := m.client.Connect(); token.Wait() && token.Error() != nil {
        return token.Error()
    }
    if token := m.client.Subscribe(m.config.Topic, 0, nil); token.Wait() && token.Error() != nil {
        return token.Error()
    }
    log.Printf("[MQTT Adapter] Subscribed to topic '%s' on broker '%s'", m.config.Topic, brokerURL)

    go func() {
        // Keep the goroutine alive
        select {}
    }()
    return nil
}

func newTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
    caCert, err := os.ReadFile(caFile)
    if err != nil {
        return nil, err
    }
    caCertPool := x509.NewCertPool()
    caCertPool.AppendCertsFromPEM(caCert)
    cert, err := tls.LoadX509KeyPair(certFile, keyFile)
    if err != nil {
        return nil, err
    }
    return &tls.Config{
        RootCAs:      caCertPool,
        Certificates: []tls.Certificate{cert},
    }, nil
}

func parseInt(v interface{}) (int, error) {
    switch val := v.(type) {
    case float64:
        return int(val), nil
    case string:
        return strconv.Atoi(val)
    default:
        return 0, fmt.Errorf("unexpected type %T", v)
    }
}
