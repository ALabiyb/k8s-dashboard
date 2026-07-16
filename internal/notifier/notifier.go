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
	ProductName   string
	OldState      string
	NewState      string
	Score         int
	HealthyCount  int
	TotalCount    int
	Timestamp     string
	UnhealthyList []string // names of unhealthy services
	DashboardURL  string   // link included in the "View dashboard" call-to-action
}

// emailTemplate is the plain-text email body — used when EmailConfig.HTMLBody is false.
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
{{ if .DashboardURL }}
View dashboard: {{ .DashboardURL }}
{{- end }}
`))

// htmlEmailTemplate is the HTML alternative — used when EmailConfig.HTMLBody is true.
// Inline styles only (email clients strip <style> blocks). Colours match the
// dashboard: red = Critical, amber = Degraded, green = Healthy.
var htmlEmailTemplate = template.Must(template.New("email-html").Parse(`<!DOCTYPE html>
<html>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px; color: #333;">
  <div style="border-left: 4px solid {{ if eq .NewState "Critical" }}#dc3545{{ else if eq .NewState "Degraded" }}#ffc107{{ else }}#28a745{{ end }}; padding: 12px 20px; background: #f8f9fa; margin-bottom: 20px;">
    <h2 style="margin: 0 0 4px 0; font-size: 18px;">K8s Dashboard Alert</h2>
    <div style="font-size: 13px; color: #666;">{{ .Timestamp }}</div>
  </div>

  <table style="width: 100%; border-collapse: collapse; margin-bottom: 20px;">
    <tr>
      <td style="padding: 8px 0; font-weight: 600; width: 120px; color: #555;">Product</td>
      <td style="padding: 8px 0;">{{ .ProductName }}</td>
    </tr>
    <tr>
      <td style="padding: 8px 0; font-weight: 600; color: #555;">State</td>
      <td style="padding: 8px 0;">
        <span style="color: #999;">{{ .OldState }}</span>
        &nbsp;→&nbsp;
        <span style="font-weight: 600; color: {{ if eq .NewState "Critical" }}#dc3545{{ else if eq .NewState "Degraded" }}#ffc107{{ else }}#28a745{{ end }};">{{ .NewState }}</span>
      </td>
    </tr>
    <tr>
      <td style="padding: 8px 0; font-weight: 600; color: #555;">Health</td>
      <td style="padding: 8px 0;">{{ .HealthyCount }}/{{ .TotalCount }} services healthy ({{ .Score }}%)</td>
    </tr>
  </table>

  {{ if .UnhealthyList }}
  <div style="background: #fff5f5; border: 1px solid #f5c6c6; padding: 12px 16px; border-radius: 4px; margin-bottom: 20px;">
    <div style="font-weight: 600; margin-bottom: 8px; color: #b03535;">Unhealthy services</div>
    <ul style="margin: 0; padding-left: 20px;">
      {{ range .UnhealthyList }}<li style="margin-bottom: 4px; font-family: 'SFMono-Regular', Menlo, Consolas, monospace; font-size: 13px;">{{ . }}</li>{{ end }}
    </ul>
  </div>
  {{ end }}

  {{ if .DashboardURL }}
  <div style="text-align: center; margin: 24px 0;">
    <a href="{{ .DashboardURL }}" style="display: inline-block; padding: 10px 24px; background: #0d6efd; color: #ffffff; text-decoration: none; border-radius: 4px; font-weight: 500;">View dashboard</a>
  </div>
  {{ end }}

  <hr style="border: none; border-top: 1px solid #eee; margin: 24px 0;">
  <div style="font-size: 12px; color: #999; text-align: center;">
    SoftNet K8s Dashboard · automated alert
  </div>
</body>
</html>
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
		DashboardURL:  n.cfg.DashboardURL,
	}

	// Pick template + Content-Type based on config.
	tmpl := emailTemplate
	contentType := "text/plain; charset=utf-8"
	if n.cfg.HTMLBody {
		tmpl = htmlEmailTemplate
		contentType = "text/html; charset=utf-8"
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, data); err != nil {
		return fmt.Errorf("rendering email template: %w", err)
	}

	// From: header. Envelope sender (MAIL FROM) MUST stay as bare n.cfg.From
	// because Zoho and most modern SMTP servers reject an envelope address
	// with a display name. The visible "From:" header inside the message body
	// is what mail clients render — so put the display name only there.
	fromHeader := n.cfg.From
	if n.cfg.FromDisplayName != "" {
		fromHeader = fmt.Sprintf("%q <%s>", n.cfg.FromDisplayName, n.cfg.From)
	}

	subject := fmt.Sprintf("[K8s Alert] %s is %s", product.DisplayName, product.Health)
	msg := buildMessage(fromHeader, n.cfg.To, subject, body.String(), contentType)

	// Connect to SMTP and send
	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)
	auth := smtp.PlainAuth("", n.cfg.SMTPUsername, n.cfg.SMTPPassword, n.cfg.SMTPHost)

	return smtp.SendMail(addr, auth, n.cfg.From, n.cfg.To, []byte(msg))
}

// buildMessage formats a raw RFC 2822 email message string.
func buildMessage(from string, to []string, subject, body, contentType string) string {
	return strings.Join([]string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: " + contentType,
		"",
		body,
	}, "\r\n")
}
