package notifications

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
)

type emailChannel struct {
	id         string
	server     string
	host       string
	from       mail.Address
	recipients []mail.Address
	username   string
	security   string
	creds      credentials.Runtime
	credID     string
}

func newEmailChannel(cfg ChannelConfig, creds credentials.Runtime) (Channel, error) {
	settings, err := parseEmailSettings(cfg)
	if err != nil {
		return nil, Permanent(err)
	}
	if settings.username != "" && (creds == nil || strings.TrimSpace(cfg.CredentialID) == "") {
		return nil, Permanent(fmt.Errorf("email: credential required when username is set"))
	}
	if settings.username == "" && strings.TrimSpace(cfg.CredentialID) != "" {
		return nil, Permanent(fmt.Errorf("email: username required when credential is set"))
	}
	return emailChannel{
		id:         cfg.ID,
		server:     settings.server,
		host:       settings.host,
		from:       settings.from,
		recipients: settings.recipients,
		username:   settings.username,
		security:   settings.security,
		creds:      creds,
		credID:     cfg.CredentialID,
	}, nil
}

type parsedEmailSettings struct {
	server     string
	host       string
	from       mail.Address
	recipients []mail.Address
	username   string
	security   string
}

func parseEmailSettings(cfg ChannelConfig) (parsedEmailSettings, error) {
	settings := cfg.Settings
	server := strings.TrimSpace(settings["server"])
	if server == "" || strings.ContainsAny(server, "\r\n") {
		return parsedEmailSettings{}, fmt.Errorf("email: server is required")
	}
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		// Port 587 is the conventional STARTTLS endpoint. Requiring an explicit
		// port is unnecessarily hostile to local SMTP relays and makes the form
		// harder to use, so accept a bare host with that default.
		host = server
		port = "587"
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return parsedEmailSettings{}, fmt.Errorf("email: server host is required")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return parsedEmailSettings{}, fmt.Errorf("email: invalid server port")
	}
	server = net.JoinHostPort(host, strconv.Itoa(portNumber))

	security := strings.ToLower(strings.TrimSpace(settings["security"]))
	if security == "" {
		security = "starttls"
	}
	switch security {
	case "starttls", "tls":
	case "plain":
		if !isLocalSMTPHost(host) {
			return parsedEmailSettings{}, fmt.Errorf("email: plain SMTP is allowed only on localhost")
		}
	default:
		return parsedEmailSettings{}, fmt.Errorf("email: security must be starttls, tls, or plain")
	}

	from, err := parseEmailAddress(settings["from"])
	if err != nil {
		return parsedEmailSettings{}, fmt.Errorf("email: from: %w", err)
	}
	recipients, err := mail.ParseAddressList(strings.TrimSpace(settings["to"]))
	if err != nil || len(recipients) == 0 {
		return parsedEmailSettings{}, fmt.Errorf("email: to must contain at least one address")
	}
	to := make([]mail.Address, 0, len(recipients))
	for _, recipient := range recipients {
		if strings.ContainsAny(recipient.Address, "\r\n") || recipient.Address == "" {
			return parsedEmailSettings{}, fmt.Errorf("email: invalid recipient")
		}
		to = append(to, *recipient)
	}

	return parsedEmailSettings{
		server: server, host: host, from: *from, recipients: to,
		username: strings.TrimSpace(settings["username"]), security: security,
	}, nil
}

func parseEmailAddress(raw string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, fmt.Errorf("invalid address")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || address.Address == "" || strings.ContainsAny(address.Address, "\r\n") {
		return nil, fmt.Errorf("invalid address")
	}
	return address, nil
}

func isLocalSMTPHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c emailChannel) ID() string { return c.id }

func (c emailChannel) Send(ctx context.Context, d Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	password := ""
	if c.username != "" {
		var err error
		password, err = c.creds.Token(ctx, c.credID)
		if err != nil {
			return err
		}
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, c.host)
	if err != nil {
		return err
	}
	defer client.Close()

	if c.security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return Permanent(fmt.Errorf("email: SMTP server does not offer STARTTLS"))
		}
		if err := client.StartTLS(&tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if c.username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.username, password, c.host)); err != nil {
			return Permanent(fmt.Errorf("email: SMTP authentication failed: %w", err))
		}
	}
	if err := client.Mail(c.from.Address); err != nil {
		return err
	}
	for _, recipient := range c.recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, formatEmailMessage(c.from, c.recipients, d.Notification)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (c emailChannel) dial(ctx context.Context) (net.Conn, error) {
	dialer := &net.Dialer{}
	if c.security == "tls" {
		return (&tls.Dialer{
			NetDialer: dialer,
			Config:    &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12},
		}).DialContext(ctx, "tcp", c.server)
	}
	return dialer.DialContext(ctx, "tcp", c.server)
}

func formatEmailMessage(from mail.Address, recipients []mail.Address, n Notification) string {
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		to = append(to, recipient.String())
	}
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(n.Title)
	if subject == "" {
		subject = "Control Center notification"
	}
	var b strings.Builder
	b.WriteString("From: ")
	b.WriteString(from.String())
	b.WriteString("\r\nTo: ")
	b.WriteString(strings.Join(to, ", "))
	b.WriteString("\r\nSubject: ")
	b.WriteString(mime.QEncoding.Encode("UTF-8", subject))
	b.WriteString("\r\nDate: ")
	b.WriteString(time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(n.Body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.String()
}
