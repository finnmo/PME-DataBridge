#!/bin/bash

echo "Stopping PME-DataBridge service..."
sudo systemctl stop pme-databridge.service

echo "Building PME-DataBridge..."
go build -o pme-databridge

echo "Setting permissions for port 502..."
sudo setcap 'cap_net_bind_service=+ep' /home/optech/PME-DataBridge/pme-databridge

echo "Starting PME-DataBridge service..."
sudo systemctl start pme-databridge.service

echo "Showing service logs (press Ctrl+C to exit)..."
journalctl -u pme-databridge.service -f 