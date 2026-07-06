// Package notifier sends email alerts when a product's health state changes.
// It only fires on transitions (e.g. Healthy→Critical), never on every poll,
// so your inbox won't be spammed. To add Slack/Telegram later, add a new
// Sender implementation here and wire it up in main.go.
package notifier

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"text/template"
	"time"

	"github.com/ALabiyb/k8s-dashboard/config"
	"github.com/ALabiyb/k8s-dashboard/internal/aggregator"
)

// Notifier handles sending alerts.
type Notifier struct {
	cfg config.EmailConfig
}

// New creates a Notifier from the email section of config.yaml.
func New(cfg config.EmailConfig) *Notifier {
	return &Notifier{cfg: cfg}
}

// CheckAndNotify iterates over all products in a summary and sends an email
// for any product whose health state has changed since the last poll.
func (n *Notifier) CheckAndNotify(summary aggregator.Summary) {
	if !n.cfg.Enabled {
		return // notifications disabled in config.yaml
	}

	for _, product := range summary.Products {
		// Only alert on state changes (or always, depending on config)
		if n.cfg.OnStateChangeOnly && !aggregator.StateChanged(product) {
			continue
		}
		if !aggregator.StateChanged(product) {
			continue
		}

		if err := n.sendEmail(product); err != nil {
			slog.Error("failed to send alert email", "component", "notifier",
				"namespace", product.Namespace, "error", err)
		} else {
			slog.Info("sent alert email", "component", "notifier",
				"namespace", product.Namespace, "transition", aggregator.FormatStateChange(product))
		}
	}
}

// emailData is passed into the email template below.
type emailData struct {
	ProductName    string
	OldState       string
	NewState       string
	Score          int
	HealthyCount   int
	TotalCount     int
	Timestamp      string
	UnhealthyList  []string // names of unhealthy services
}

// emailTemplate is the plain-text email body.
// To make it HTML, change smtp.SendMail content-type and use html/template.
var emailTemplate = template.Must(template.New("email").Parse(`
K8s Dashboard Alert
===================
Product:   {{ .ProductName }}
State:     {{ .OldState }} → {{ .NewState }}
Score:     {{ .HealthyCount }}/{{ .TotalCount }} services healthy ({{ .Score }}%)
Time:      {{ .Timestamp }}

{{ if .UnhealthyList -}}
Unhealthy services:
{{ range .UnhealthyList }}  - {{ . }}
{{ end }}
{{- end }}

View dashboard: http://k8s-dashboard.your-domain.com
`))

// sendEmail builds and sends a single alert email for one product.
func (n *Notifier) sendEmail(product aggregator.ProductHealth) error {
	// Collect names of unhealthy services for the email body
	var unhealthy []string
	for _, svc := range product.Services {
		if svc.Status != "Healthy" {
			unhealthy = append(unhealthy, fmt.Sprintf("%s (%s) — %s", svc.Name, svc.Kind, svc.Reason))
		}
	}

	data := emailData{
		ProductName:   product.DisplayName,
		OldState:      string(product.PreviousHealth),
		NewState:      string(product.Health),
		Score:         product.ScorePercent,
		HealthyCount:  product.HealthyCount,
		TotalCount:    product.TotalCount,
		Timestamp:     time.Now().Format("2006-01-02 15:04:05 UTC"),
		UnhealthyList: unhealthy,
	}

	// Render the email body from the template
	var body bytes.Buffer
	if err := emailTemplate.Execute(&body, data); err != nil {
		return fmt.Errorf("rendering email template: %w", err)
	}

	// Build the raw email message with headers
	subject := fmt.Sprintf("[K8s Alert] %s is %s", product.DisplayName, product.Health)
	msg := buildMessage(n.cfg.From, n.cfg.To, subject, body.String())

	// Connect to SMTP and send
	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)
	auth := smtp.PlainAuth("", n.cfg.SMTPUsername, n.cfg.SMTPPassword, n.cfg.SMTPHost)

	return smtp.SendMail(addr, auth, n.cfg.From, n.cfg.To, []byte(msg))
}

// buildMessage formats a raw RFC 2822 email message string.
func buildMessage(from string, to []string, subject, body string) string {
	return strings.Join([]string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")
}
