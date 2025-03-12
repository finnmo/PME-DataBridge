package plugins

import "log"

// Data represents normalized data from an adapter.
type Data struct {
	UnitID         int   // Target Modbus unit ID
	ModbusRegister int   // Conventional register address (e.g. 40122)
	Liters         int32 // Measurement value (converted to a 32-bit integer)
}

// Adapter is the interface that every input plugin must implement.
type Adapter interface {
	// Name returns the name of the adapter.
	Name() string
	// Start begins processing and sends normalized data on the provided channel.
	Start(dataCh chan<- Data) error
}

// Optionally, you can maintain a registry if desired.
var adapterRegistry = make(map[string]func() Adapter)

func RegisterAdapter(name string, constructor func() Adapter) {
	adapterRegistry[name] = constructor
	log.Printf("[PLUGIN] Registered adapter: %s", name)
}

func GetAdapter(name string) (Adapter, bool) {
	ctor, ok := adapterRegistry[name]
	if !ok {
		return nil, false
	}
	return ctor(), true
}
