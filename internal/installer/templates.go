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
		"MITHRA_RESEND_FROM=" + strconv.Quote(plan.Options.ResendFrom),
	}
	if plan.Proxy == AppOnly {
		lines = append(lines, "MITHRA_ADDR="+plan.Listener)
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
LoadCredential=resend.key:/etc/mithra/credentials/resend.key
Environment=MITHRA_MASTER_KEY_FILE=%d/master.key
Environment=MITHRA_RESEND_KEY_FILE=%d/resend.key
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

func ProxyConfig(plan Plan) string {
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
