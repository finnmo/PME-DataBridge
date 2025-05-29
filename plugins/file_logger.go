package plugins

import (
	"encoding/json"
	"log"
	"os"
)

// FileLogger is a plugin that writes normalized data to a file.
type FileLogger struct {
	FilePath string
	file     *os.File
	logger   *log.Logger
}

// NewFileLogger creates a new FileLogger for the given file path.
func NewFileLogger(filePath string) *FileLogger {
	return &FileLogger{
		FilePath: filePath,
	}
}

// Start begins logging data from the provided channel to the file.
// It implements a simple consumer that listens indefinitely on the channel.
func (fl *FileLogger) Start(dataCh <-chan Data) error {
	var err error
	fl.file, err = os.OpenFile(fl.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	// Create a logger that writes to the file.
	fl.logger = log.New(fl.file, "", log.LstdFlags)
	
	go func() {
		defer fl.file.Close()
		for d := range dataCh {
			// Marshal the normalized data to JSON.
			b, err := json.Marshal(d)
			if err != nil {
				fl.logger.Printf("Error marshaling data: %v", err)
				continue
			}
			fl.logger.Printf("Data: %s", string(b))
		}
	}()
	return nil
}
