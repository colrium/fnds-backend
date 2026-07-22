package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/configs"
	"backend/integrations"
)

type deliveryStore interface {
	ClaimDelivery(context.Context) (*Notification, error)
	MarkDeliverySent(context.Context, string) error
	MarkDeliveryFailed(context.Context, string, error) error
}

type emailSender interface {
	Send(context.Context, string, string, string) error
}

type smsSender interface {
	Send(string, string) error
}

type Dispatcher struct {
	store deliveryStore
	email emailSender
	sms   smsSender
}

func NewDispatcher(store deliveryStore, email emailSender, sms smsSender) *Dispatcher {
	return &Dispatcher{store: store, email: email, sms: sms}
}

func NewDefaultDispatcher() *Dispatcher {
	return NewDispatcher(
		NewMongoStore(),
		integrations.NewSendGridClient(
			configs.SendGridAPIKey(),
			configs.SendGridFromEmail(),
			configs.SendGridFromName(),
		),
		&AdvantaSMS{},
	)
}

func (d *Dispatcher) ProcessOne(ctx context.Context) (bool, error) {
	notification, err := d.store.ClaimDelivery(ctx)
	if err != nil || notification == nil {
		return false, err
	}
	if notification.Delivery == nil {
		return true, d.store.MarkDeliveryFailed(
			ctx,
			notification.ID,
			fmt.Errorf("notification delivery is missing"),
		)
	}

	delivery := notification.Delivery
	var sendErr error
	switch delivery.Channel {
	case DeliveryChannelEmail:
		sendErr = d.email.Send(ctx, delivery.To, delivery.Subject, delivery.Body)
	case DeliveryChannelSMS:
		sendErr = d.sms.Send(delivery.To, delivery.Body)
	default:
		sendErr = fmt.Errorf("unsupported notification delivery channel %q", delivery.Channel)
	}
	if sendErr != nil {
		markErr := d.store.MarkDeliveryFailed(ctx, notification.ID, sendErr)
		return true, errors.Join(sendErr, markErr)
	}
	return true, d.store.MarkDeliverySent(ctx, notification.ID)
}

func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		d.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) drain(ctx context.Context) {
	for range 10 {
		processed, err := d.ProcessOne(ctx)
		if err != nil {
			slog.Error("notification delivery failed", "error", err)
		}
		if !processed || ctx.Err() != nil {
			return
		}
	}
}
