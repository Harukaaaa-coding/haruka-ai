package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"GopherAI/config"

	"gopkg.in/gomail.v2"
)

const (
	CodeMsg     = "GopherAI验证码如下(验证码仅限于2分钟有效): "
	UserNameMsg = "GopherAI的账号如下，请保留好，后续可以用账号/邮箱登录 "

	smtpHost             = "smtp.163.com"
	smtpPort             = 465
	smtpOperationTimeout = 10 * time.Second
	smtpConcurrentLimit  = 4
)

type smtpSettings struct {
	host     string
	port     int
	username string
	password string
}

type smtpDeliveryFunc func(context.Context, smtpSettings, string, *gomail.Message) error

var (
	smtpSendSlots        = make(chan struct{}, smtpConcurrentLimit)
	smtpSettingsProvider = configuredSMTPSettings
	smtpDelivery         = deliverSMTP
)

// SendCaptcha preserves the original API for callers without a request
// context. The operation itself is still bounded by smtpOperationTimeout.
func SendCaptcha(recipient, code, message string) error {
	return SendCaptchaContext(context.Background(), recipient, code, message)
}

// SendCaptchaContext sends one message with a bounded SMTP operation. gomail's
// DialAndSend API cannot be cancelled once it starts, so this implementation
// owns the TCP connection: context-aware dialing plus a connection deadline
// bounds TLS, authentication and message delivery without leaving an
// unbounded timeout goroutine behind. A small process-wide semaphore prevents
// a mail-provider outage from consuming every request worker.
func SendCaptchaContext(ctx context.Context, recipient, code, message string) error {
	ctx, cancel := withSMTPTimeout(ctx)
	defer cancel()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("SMTP operation canceled: %w", err)
	}

	settings := smtpSettingsProvider()
	mailMessage, sender, envelopeRecipient, err := newCaptchaMessage(settings, recipient, code, message)
	if err != nil {
		return err
	}
	settings.username = sender

	release, err := acquireSMTPSlot(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := smtpDelivery(ctx, settings, envelopeRecipient, mailMessage); err != nil {
		return fmt.Errorf("send SMTP message: %w", err)
	}
	return nil
}

func configuredSMTPSettings() smtpSettings {
	conf := config.GetConfig()
	return smtpSettings{
		host:     smtpHost,
		port:     smtpPort,
		username: strings.TrimSpace(conf.EmailConfig.Email),
		password: conf.EmailConfig.Authcode,
	}
}

func withSMTPTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, smtpOperationTimeout)
}

func acquireSMTPSlot(ctx context.Context) (func(), error) {
	select {
	case smtpSendSlots <- struct{}{}:
		return func() { <-smtpSendSlots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("SMTP delivery is busy: %w", ctx.Err())
	}
}

func newCaptchaMessage(settings smtpSettings, recipient, code, message string) (*gomail.Message, string, string, error) {
	from, err := normalizedAddress(settings.username)
	if err != nil {
		return nil, "", "", fmt.Errorf("SMTP sender configuration: %w", err)
	}
	to, err := normalizedAddress(recipient)
	if err != nil {
		return nil, "", "", fmt.Errorf("SMTP recipient: %w", err)
	}
	if strings.TrimSpace(settings.password) == "" {
		return nil, "", "", fmt.Errorf("SMTP credentials are not configured")
	}

	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", "来自GopherAI的信息")
	m.SetBody("text/plain", strings.TrimSpace(message)+" "+strings.TrimSpace(code))
	return m, from, to, nil
}

func normalizedAddress(value string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || address.Address == "" {
		return "", fmt.Errorf("invalid email address")
	}
	return address.Address, nil
}

func deliverSMTP(ctx context.Context, settings smtpSettings, recipient string, message *gomail.Message) error {
	if strings.TrimSpace(settings.host) == "" || settings.port < 1 || settings.port > 65535 {
		return fmt.Errorf("invalid SMTP server configuration")
	}
	if message == nil {
		return fmt.Errorf("SMTP message is required")
	}
	recipient, err := normalizedAddress(recipient)
	if err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}

	address := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dial SMTP server: %w", err)
	}
	defer connection.Close()
	// A deadline bounds the full operation. AfterFunc additionally makes an
	// earlier parent cancellation interrupt an in-progress AUTH or DATA call
	// immediately instead of waiting for that deadline to elapse.
	stopCancelWatcher := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancelWatcher()

	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set SMTP deadline: %w", err)
		}
	}

	tlsConnection := tls.Client(connection, &tls.Config{
		ServerName: settings.host,
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("SMTP TLS handshake: %w", err)
	}

	client, err := smtp.NewClient(tlsConnection, settings.host)
	if err != nil {
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(smtp.PlainAuth("", settings.username, settings.password, settings.host)); err != nil {
		return fmt.Errorf("SMTP authentication: %w", err)
	}
	if err := client.Mail(settings.username); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}

	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := message.WriteTo(writer); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close SMTP session: %w", err)
	}
	return nil
}
