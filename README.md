###Build: 
`go build -o pme-databridge`

###Show Services Logs: 
`journalctl -u pme-databridge.service -f`

###Stop Service:
`sudo systemctl stop pme-databridge.service`

###Start Service:
`sudo systemctl start pme-databridge.service`

###Restart Service:
`sudo systemctl restart pme-databridge.service`

###Give permissions for 502 port:
`sudo setcap 'cap_net_bind_service=+ep' /home/optech/PME-DataBridge/pme-databridge`