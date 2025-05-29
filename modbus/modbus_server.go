package modbus

import (
    "encoding/binary"
    "io"
    "log"
    "net"
    "sync"
)

// SlaveContext holds the holding registers for a Modbus unit.
type SlaveContext struct {
    HR []uint16
    Mu sync.RWMutex  // <-- Must be capital "M" to export.
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

// UpdateRegister writes a 32-bit signed integer (SINT32) in **big-endian** order
// across two 16-bit registers. This matches how the Python code does it with
// builder.add_32bit_int(value, byteorder=Endian.BIG, wordorder=Endian.BIG).
func (sc *ServerContext) UpdateRegister(unitID int, registerOffset int, value int32) {
    slave, ok := sc.Slaves[unitID]
    if !ok {
        log.Printf("Unit id %d not present in pre-allocated slaves.", unitID)
        return
    }

    // Convert int32 to two uint16 values (big endian).
    buf := make([]byte, 4)
    binary.BigEndian.PutUint32(buf, uint32(value))
    r1 := binary.BigEndian.Uint16(buf[0:2]) // high word
    r2 := binary.BigEndian.Uint16(buf[2:4]) // low word

    slave.Mu.Lock()
    defer slave.Mu.Unlock()

    if registerOffset < 0 || registerOffset+1 >= len(slave.HR) {
        log.Printf("Register offset %d out of range for unit %d.", registerOffset, unitID)
        return
    }
    slave.HR[registerOffset] = r1
    slave.HR[registerOffset+1] = r2

    log.Printf("[Modbus] Updated unit %d at offset %d with value %d.", unitID, registerOffset, value)
}

// StartTCPServer handles read requests (function code 3) in big-endian order.
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
        go handleConnection(conn, ctx)
    }
}

func handleConnection(c net.Conn, ctx *ServerContext) {
    defer c.Close()
    log.Printf("Accepted connection from %s", c.RemoteAddr())

    for {
        // Read MBAP header (7 bytes)
        header := make([]byte, 7)
        _, err := io.ReadFull(c, header)
        if err != nil {
            log.Printf("Error reading MBAP header: %v", err)
            return
        }

        transactionID := binary.BigEndian.Uint16(header[0:2])
        protocolID := binary.BigEndian.Uint16(header[2:4])
        length := binary.BigEndian.Uint16(header[4:6])
        unitID := header[6]

        if protocolID != 0 {
            log.Printf("Unexpected protocol ID: %d", protocolID)
            return
        }

        // length includes unit ID + PDU
        pduLength := int(length) - 1
        pdu := make([]byte, pduLength)
        _, err = io.ReadFull(c, pdu)
        if err != nil {
            log.Printf("Error reading PDU: %v", err)
            return
        }
        if len(pdu) < 5 {
            log.Printf("PDU too short: %v", pdu)
            return
        }

        functionCode := pdu[0]
        switch functionCode {
        case 3: // Read Holding Registers
            startAddress := binary.BigEndian.Uint16(pdu[1:3])
            quantity := binary.BigEndian.Uint16(pdu[3:5])

            log.Printf("Received Read Holding Registers: UnitID %d, Start Address %d, Quantity %d",
                unitID, startAddress, quantity)

            slave, ok := ctx.Slaves[int(unitID)]
            if !ok {
                log.Printf("No slave for unit ID %d", unitID)
                sendExceptionResponse(c, transactionID, unitID, functionCode, 0x0B)
                continue
            }

            start := int(startAddress)
            reqQty := int(quantity)

            slave.Mu.RLock()
            if start+reqQty > len(slave.HR) {
                log.Printf("Requested registers out of range: start %d, quantity %d", start, reqQty)
                slave.Mu.RUnlock()
                sendExceptionResponse(c, transactionID, unitID, functionCode, 0x02)
                continue
            }
            registers := slave.HR[start : start+reqQty]
            slave.Mu.RUnlock()

            // Build normal response
            byteCount := uint8(len(registers) * 2)
            responsePDU := make([]byte, 2+len(registers)*2)
            responsePDU[0] = functionCode
            responsePDU[1] = byteCount

            for i, reg := range registers {
                binary.BigEndian.PutUint16(responsePDU[2+i*2:2+i*2+2], reg)
            }

            // Build MBAP header
            responseLength := uint16(len(responsePDU) + 1) // +1 for unitID
            responseHeader := make([]byte, 7)
            binary.BigEndian.PutUint16(responseHeader[0:2], transactionID)
            binary.BigEndian.PutUint16(responseHeader[2:4], 0)
            binary.BigEndian.PutUint16(responseHeader[4:6], responseLength)
            responseHeader[6] = unitID

            response := append(responseHeader, responsePDU...)
            _, err = c.Write(response)
            if err != nil {
                log.Printf("Error writing response: %v", err)
                return
            }
            log.Printf("Sent response for transaction %d", transactionID)

        default:
            log.Printf("Unsupported function code: %d", functionCode)
            sendExceptionResponse(c, transactionID, unitID, functionCode, 0x01)
        }
    }
}

func sendExceptionResponse(c net.Conn, transactionID uint16, unitID byte, functionCode byte, exceptionCode byte) {
    exceptionFunctionCode := functionCode | 0x80
    pdu := []byte{exceptionFunctionCode, exceptionCode}
    responseLength := uint16(len(pdu) + 1)
    header := make([]byte, 7)
    binary.BigEndian.PutUint16(header[0:2], transactionID)
    binary.BigEndian.PutUint16(header[2:4], 0)
    binary.BigEndian.PutUint16(header[4:6], responseLength)
    header[6] = unitID

    response := append(header, pdu...)
    _, err := c.Write(response)
    if err != nil {
        log.Printf("Error writing exception response: %v", err)
    }
}
