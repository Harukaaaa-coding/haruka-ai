package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/gomail.v2"
)

func replaceSMTPHooks(t *testing.T, settings smtpSettings, delivery smtpDeliveryFunc) {
	t.Helper()
	previousSettingsProvider := smtpSettingsProvider
	previousDelivery := smtpDelivery
	smtpSettingsProvider = func() smtpSettings { return settings }
	smtpDelivery = delivery
	t.Cleanup(func() {
		smtpSettingsProvider = previousSettingsProvider
		smtpDelivery = previousDelivery
	})
}

func testSMTPSettings() smtpSettings {
	return smtpSettings{
		host:     "smtp.example.test",
		port:     465,
		username: "sender@example.test",
		password: "test-password",
	}
}

func TestSendCaptchaContextPassesBoundedContextAndValidatedEnvelope(t *testing.T) {
	settings := testSMTPSettings()
	called := false
	replaceSMTPHooks(t, settings, func(ctx context.Context, received smtpSettings, recipient string, message *gomail.Message) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("SMTP delivery did not receive a deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > smtpOperationTimeout {
			t.Fatalf("SMTP deadline remaining = %s, want within operation timeout", remaining)
		}
		if received.username != "sender@example.test" || recipient != "recipient@example.test" {
			t.Fatalf("unexpected SMTP envelope: settings=%#v recipient=%q", received, recipient)
		}
		var serialized strings.Builder
		if _, err := message.WriteTo(&serialized); err != nil {
			t.Fatalf("serialize message: %v", err)
		}
		for _, want := range []string{"From: sender@example.test", "To: recipient@example.test", "captcha message 123456"} {
			if !strings.Contains(serialized.String(), want) {
				t.Fatalf("serialized message missing %q: %s", want, serialized.String())
			}
		}
		return nil
	})

	if err := SendCaptchaContext(context.Background(), "recipient@example.test", "123456", "captcha message"); err != nil {
		t.Fatalf("SendCaptchaContext() error = %v", err)
	}
	if !called {
		t.Fatal("SMTP delivery was not called")
	}
}

func TestSendCaptchaContextStopsBeforeDeliveryWhenCanceled(t *testing.T) {
	called := false
	replaceSMTPHooks(t, testSMTPSettings(), func(context.Context, smtpSettings, string, *gomail.Message) error {
		called = true
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := SendCaptchaContext(ctx, "recipient@example.test", "123456", "captcha message")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendCaptchaContext() error = %v, want context cancellation", err)
	}
	if called {
		t.Fatal("canceled SMTP operation reached delivery")
	}
}

func TestSendCaptchaContextRespectsGlobalDeliveryLimit(t *testing.T) {
	replaceSMTPHooks(t, testSMTPSettings(), func(context.Context, smtpSettings, string, *gomail.Message) error {
		t.Fatal("SMTP delivery must not start while all slots are occupied")
		return nil
	})
	for i := 0; i < cap(smtpSendSlots); i++ {
		smtpSendSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(smtpSendSlots); i++ {
			<-smtpSendSlots
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := SendCaptchaContext(ctx, "recipient@example.test", "123456", "captcha message")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendCaptchaContext() error = %v, want delivery-limit timeout", err)
	}
}
