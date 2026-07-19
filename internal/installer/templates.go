package installer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func RuntimeConfig(plan Plan) string {
	emails := append([]string(nil), plan.Options.AllowedEmails...)
	sort.Strings(emails)
	lines := []string{
		"ALLOWED_EMAILS=" + strings.Join(emails, ","),
		"MITHRA_DB=/var/lib/mithra/mithra.sqlite3",
		"MITHRA_SOURCE_DIR=/var/lib/mithra/sources",
		"MITHRA_PROXY_MODE=" + string(plan.Proxy),
		"MITHRA_PLUNK_FROM=" + strconv.Quote(plan.Options.PlunkFrom),
	}
	if plan.Proxy == AppOnly {
		lines = append(lines, "MITHRA_ADDR="+plan.Listener, "MITHRA_CANONICAL_ORIGIN=http://"+plan.Listener)
	} else {
		lines = append(lines, "MITHRA_SOCKET=/run/mithra/mithra.sock", "MITHRA_CANONICAL_ORIGIN=https://"+plan.Options.Domain)
	}
	return strings.Join(lines, "\n") + "\n"
}

func ServiceUnit() string {
	return `[Unit]
Description=Mithra household application
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mithra
Group=mithra
EnvironmentFile=/etc/mithra/mithra.env
LoadCredential=master.key:/etc/mithra/credentials/master.key
LoadCredential=plunk.key:/etc/mithra/credentials/plunk.key
Environment=MITHRA_MASTER_KEY_FILE=%d/master.key
Environment=MITHRA_PLUNK_KEY_FILE=%d/plunk.key
ExecStart=/usr/local/bin/mithra
RuntimeDirectory=mithra
RuntimeDirectoryMode=0750
ReadWritePaths=/var/lib/mithra /var/backups/mithra /run/mithra
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=true
MemoryDenyWriteExecute=true
Restart=on-failure
RestartSec=3
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`
}

func BackupServiceUnit() string {
	return `[Unit]
Description=Create one authenticated Mithra backup generation
After=mithra.service

[Service]
Type=oneshot
User=root
ExecStart=/usr/local/bin/mithra-installer backup
Nice=10
IOSchedulingClass=idle
`
}

func BackupTimerUnit() string {
	return `[Unit]
Description=Daily Mithra backup

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=20m

[Install]
WantedBy=timers.target
`
}

func PDFParserServiceUnit() string {
	return `[Unit]
Description=Mithra isolated PDF parser
Requires=mithra-pdf-parser.socket
After=mithra-pdf-parser.socket

[Service]
Type=simple
ExecStart=/usr/local/bin/mithra pdf-parser
User=mithra-pdf
Group=mithra
UMask=0077
NoNewPrivileges=true
PrivateNetwork=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
InaccessiblePaths=/var/lib/mithra/mithra.sqlite3 /var/lib/mithra/sources /etc/mithra/credentials
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectProc=invisible
RestrictAddressFamilies=AF_UNIX
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
LimitNOFILE=64
MemoryMax=192M
CPUQuota=50%
TimeoutStartSec=15s
TimeoutStopSec=5s
RuntimeMaxSec=5min
Restart=on-failure
RestartSec=1s

[Install]
WantedBy=multi-user.target
`
}

func PDFParserSocketUnit() string {
	return `[Unit]
Description=Mithra isolated PDF parser socket

[Socket]
ListenStream=/run/mithra/pdf-parser.sock
SocketMode=0660
SocketUser=mithra
SocketGroup=mithra
RemoveOnStop=true

[Install]
WantedBy=sockets.target
`
}

func ProxyConfig(plan Plan) string {
	if _, err := canonicalHostname(plan.Options.Domain); err != nil {
		return ""
	}
	switch plan.Proxy {
	case Caddy:
		return fmt.Sprintf("%s {\n\treverse_proxy unix//run/mithra/mithra.sock\n}\n", plan.Options.Domain)
	case Nginx:
		return fmt.Sprintf("server {\n    listen 443 ssl;\n    server_name %s;\n    location / { proxy_pass http://unix:/run/mithra/mithra.sock:; proxy_set_header Host $host; }\n}\n", plan.Options.Domain)
	case Apache:
		return fmt.Sprintf("<VirtualHost *:443>\nServerName %s\nProxyPass / unix:/run/mithra/mithra.sock|http://localhost/\nProxyPassReverse / unix:/run/mithra/mithra.sock|http://localhost/\n</VirtualHost>\n", plan.Options.Domain)
	default:
		return ""
	}
}
