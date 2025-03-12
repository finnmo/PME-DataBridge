package modbus

import (
	"encoding/binary"
	"log"
	"net"
	"sync"
)

// SlaveContext holds the holding registers for a Modbus unit.
type SlaveContext struct {
	HR []uint16
	mu sync.RWMutex
}

// ServerContext holds all the slave contexts.
type ServerContext struct {
	Slaves map[int]*SlaveContext
}

// NewServerContext pre-allocates slave contexts for unit IDs 1 to 100.
func NewServerContext() *ServerContext {
	slaves := make(map[int]*SlaveContext)
	for unit := 1; unit <= 100; unit++ {
		slaves[unit] = &SlaveContext{
			HR: make([]uint16, 5000),
		}
	}
	return &ServerContext{Slaves: slaves}
}

// UpdateRegister writes a 32-bit signed integer (using 2 registers) into the specified unit.
// The zero-based register offset is computed as: offset = conventional register - 400001.
func (sc *ServerContext) UpdateRegister(unitID int, registerOffset int, value int32) {
	slave, ok := sc.Slaves[unitID]
	if !ok {
		log.Printf("Unit id %d not present in pre-allocated slaves.", unitID)
		return
	}
	// Convert int32 to two uint16 values (big endian).
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(value))
	r1 := binary.BigEndian.Uint16(buf[0:2])
	r2 := binary.BigEndian.Uint16(buf[2:4])
	slave.mu.Lock()
	defer slave.mu.Unlock()
	if registerOffset < 0 || registerOffset+1 >= len(slave.HR) {
		log.Printf("Register offset %d out of range for unit %d.", registerOffset, unitID)
		return
	}
	slave.HR[registerOffset] = r1
	slave.HR[registerOffset+1] = r2
	log.Printf("[Modbus] Updated unit %d at offset %d (conventional address %d) with value %d.",
		unitID, registerOffset, registerOffset+400001, value)
}

// StartTCPServer is a stub implementation of a Modbus TCP server.
// In production, replace this with a full-featured Modbus TCP server library.
func StartTCPServer(ctx *ServerContext, address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	log.Printf("Modbus TCP server listening on %s", address)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %v", err)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			log.Printf("Accepted connection from %s", c.RemoteAddr())
			// For now, simply hold the connection open.
			select {}
		}(conn)
	}
}
