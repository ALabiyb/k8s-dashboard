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
	_ "time/tzdata" // embed the tz database so LoadLocation works on tzdata-less alpine

	"github.com/ALabiyb/k8s-dashboard/config"
	"github.com/ALabiyb/k8s-dashboard/internal/aggregator"
)

// alertTZ is the display timezone for email timestamps. Falls back to UTC
// when the tzdata database can't resolve Africa/Dar_es_Salaam (rare — alpine
// images without tzdata installed).
var alertTZ = func() *time.Location {
	loc, err := time.LoadLocation("Africa/Dar_es_Salaam")
	if err != nil {
		return time.UTC
	}
	return loc
}()

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
//
// State-aware theming: header gradient, badge, and icon all recolor by state.
// Dark-body-safe: the outer card sits on a light neutral card, so the email
// renders identically in Gmail dark mode and light mode.
var htmlEmailTemplate = template.Must(template.New("email-html").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>K8s Dashboard Alert</title>
</head>
<body style="margin:0;padding:0;background:#eef2f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Arial,sans-serif;color:#1f2937;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#eef2f7;padding:32px 12px;">
    <tr>
      <td align="center">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;background:#ffffff;border-radius:14px;overflow:hidden;box-shadow:0 4px 20px rgba(15,23,42,0.08);">

          <!-- HEADER: state-tinted gradient with emoji icon + title -->
          <tr>
            <td style="padding:28px 32px 20px;background:linear-gradient(135deg,{{ if eq .NewState "Critical" }}#c53030 0%,#7f1d1d 100%{{ else if eq .NewState "Degraded" }}#c98a1a 0%,#7c4a0d 100%{{ else }}#16a34a 0%,#166534 100%{{ end }});color:#ffffff;">
              <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
                <tr>
                  <td style="font-size:32px;line-height:1;padding-right:14px;width:44px;vertical-align:middle;">
                    {{ if eq .NewState "Critical" }}🚨{{ else if eq .NewState "Degraded" }}⚠️{{ else }}✅{{ end }}
                  </td>
                  <td style="vertical-align:middle;">
                    <div style="font-size:12px;letter-spacing:2px;text-transform:uppercase;opacity:0.85;font-weight:600;">K8s Dashboard · Alert</div>
                    <div style="font-size:22px;font-weight:700;margin-top:4px;line-height:1.25;">{{ .ProductName }} is {{ .NewState }}</div>
                    <div style="font-size:13px;opacity:0.9;margin-top:6px;">{{ .Timestamp }}</div>
                  </td>
                </tr>
              </table>
            </td>
          </tr>

          <!-- STATE PILL + SCORE STRIP -->
          <tr>
            <td style="padding:20px 32px 0;">
              <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
                <tr>
                  <td>
                    <span style="display:inline-block;padding:5px 12px;border-radius:999px;font-size:12px;font-weight:600;color:#6b7280;background:#e5e7eb;">{{ .OldState }}</span>
                    <span style="padding:0 6px;color:#9ca3af;font-size:14px;">→</span>
                    <span style="display:inline-block;padding:5px 12px;border-radius:999px;font-size:12px;font-weight:700;color:#ffffff;background:{{ if eq .NewState "Critical" }}#dc2626{{ else if eq .NewState "Degraded" }}#d97706{{ else }}#16a34a{{ end }};">{{ .NewState }}</span>
                  </td>
                  <td align="right" style="font-size:13px;color:#6b7280;">
                    <span style="font-weight:700;color:#111827;font-size:16px;">{{ .HealthyCount }}/{{ .TotalCount }}</span>
                    &nbsp;services healthy · <span style="font-weight:700;">{{ .Score }}%</span>
                  </td>
                </tr>
              </table>
              <!-- health bar -->
              <div style="margin-top:14px;height:6px;background:#e5e7eb;border-radius:999px;overflow:hidden;">
                <div style="height:6px;width:{{ .Score }}%;background:{{ if eq .NewState "Critical" }}#dc2626{{ else if eq .NewState "Degraded" }}#d97706{{ else }}#16a34a{{ end }};"></div>
              </div>
            </td>
          </tr>

          {{ if .UnhealthyList }}
          <!-- UNHEALTHY SERVICES CARD -->
          <tr>
            <td style="padding:24px 32px 0;">
              <div style="background:#fef2f2;border:1px solid #fecaca;border-radius:10px;padding:16px 18px;">
                <div style="display:flex;align-items:center;gap:8px;font-size:13px;font-weight:700;color:#991b1b;text-transform:uppercase;letter-spacing:1px;margin-bottom:12px;">
                  <span style="font-size:16px;">🔻</span> Unhealthy services · {{ len .UnhealthyList }}
                </div>
                <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
                  {{ range .UnhealthyList }}
                  <tr>
                    <td style="padding:6px 0;font-family:'SFMono-Regular',Menlo,Consolas,monospace;font-size:13px;color:#7f1d1d;">
                      <span style="color:#dc2626;margin-right:8px;">▸</span>{{ . }}
                    </td>
                  </tr>
                  {{ end }}
                </table>
              </div>
            </td>
          </tr>
          {{ end }}

          {{ if .DashboardURL }}
          <!-- CTA -->
          <tr>
            <td align="center" style="padding:28px 32px 8px;">
              <a href="{{ .DashboardURL }}" style="display:inline-block;padding:12px 28px;background:#111827;color:#ffffff;text-decoration:none;border-radius:8px;font-weight:600;font-size:14px;letter-spacing:0.3px;box-shadow:0 2px 8px rgba(17,24,39,0.2);">
                Open dashboard →
              </a>
              <div style="margin-top:10px;font-size:12px;color:#6b7280;">Or copy: <span style="font-family:'SFMono-Regular',Menlo,Consolas,monospace;color:#374151;">{{ .DashboardURL }}</span></div>
            </td>
          </tr>
          {{ end }}

          <!-- FOOTER -->
          <tr>
            <td style="padding:24px 32px 28px;">
              <div style="border-top:1px solid #e5e7eb;padding-top:16px;text-align:center;font-size:12px;color:#9ca3af;line-height:1.6;">
                <div style="font-weight:600;color:#6b7280;">SoftNet K8s Dashboard</div>
                <div>Automated alert · fires only on health state transitions</div>
              </div>
            </td>
          </tr>

        </table>
      </td>
    </tr>
  </table>
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
		Timestamp:     time.Now().In(alertTZ).Format("Mon, 02 Jan 2006 · 15:04:05 MST"),
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
