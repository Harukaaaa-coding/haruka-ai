package user

import (
	myemail "GopherAI/common/email"
	userdao "GopherAI/dao/user"
	"context"
	"log"
	"sync"
	"time"
)

var (
	registrationEmailWake     = make(chan struct{}, 1)
	registrationEmailWorkerMu sync.Mutex
	registrationEmailRunning  bool
	registrationEmailDone     chan struct{}
)

func StartRegistrationEmailWorker(ctx context.Context) {
	registrationEmailWorkerMu.Lock()
	defer registrationEmailWorkerMu.Unlock()
	if registrationEmailRunning {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	registrationEmailRunning = true
	registrationEmailDone = make(chan struct{})
	done := registrationEmailDone
	go func() {
		defer func() {
			registrationEmailWorkerMu.Lock()
			registrationEmailRunning = false
			close(done)
			registrationEmailWorkerMu.Unlock()
		}()
		registrationEmailLoop(ctx)
	}()
}

func RegistrationEmailWorkerRunning() bool {
	registrationEmailWorkerMu.Lock()
	defer registrationEmailWorkerMu.Unlock()
	return registrationEmailRunning
}

func WaitRegistrationEmailWorker(ctx context.Context) error {
	registrationEmailWorkerMu.Lock()
	done := registrationEmailDone
	registrationEmailWorkerMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NotifyRegistrationEmailWorker() {
	select {
	case registrationEmailWake <- struct{}{}:
	default:
	}
}

func registrationEmailLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		processRegistrationEmails(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-registrationEmailWake:
		}
	}
}

func processRegistrationEmails(ctx context.Context) {
	users, err := userdao.ListPendingRegistrationEmails(ctx, 20)
	if err != nil {
		log.Printf("list pending registration emails: %v", err)
		return
	}
	for i := range users {
		if err := ctx.Err(); err != nil {
			return
		}
		pending := &users[i]
		if err := myemail.SendCaptchaContext(ctx, pending.Email, pending.Username, myemail.UserNameMsg); err != nil {
			nextAttempt := time.Now().Add(registrationEmailRetryDelay(pending.RegistrationEmailAttempts + 1))
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			markErr := userdao.MarkRegistrationEmailFailed(cleanupCtx, pending.ID, nextAttempt)
			cancel()
			if markErr != nil {
				log.Printf("schedule registration email retry for user %d: %v", pending.ID, markErr)
			}
			log.Printf("registration email delivery failed for user %d; retry scheduled: %v", pending.ID, err)
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		markErr := userdao.MarkRegistrationEmailSent(cleanupCtx, pending.ID)
		cancel()
		if markErr != nil {
			log.Printf("mark registration email sent for user %d: %v", pending.ID, markErr)
		}
	}
}

func registrationEmailRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}
