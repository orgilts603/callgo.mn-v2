package mailer

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"strings"
	texttemplate "text/template"
	"time"
)

// Message is a rendered email.
type Message struct {
	Subject string
	Text    string
	HTML    string
}

// Templates renders the bilingual (Mongolian first, then English)
// transactional emails. AppURL is the public dashboard origin, e.g.
// "https://app.callgo.mn" (CALLGO_APP_URL).
type Templates struct {
	AppURL  string
	Product string // defaults to "CallGo.mn"
}

// NewTemplates returns templates linking to appURL.
func NewTemplates(appURL string) Templates {
	return Templates{AppURL: strings.TrimRight(appURL, "/"), Product: "CallGo.mn"}
}

// Link builds "<AppURL><path>?token=<token>".
func (t Templates) Link(path, token string) string {
	base := strings.TrimRight(t.AppURL, "/")
	if base == "" {
		base = "http://localhost:5173"
	}
	return base + path + "?token=" + url.QueryEscape(token)
}

func (t Templates) product() string {
	if t.Product == "" {
		return "CallGo.mn"
	}
	return t.Product
}

// section is one language block of an email.
type section struct {
	Greeting string
	Lines    []string
	Button   string
	Footer   []string
}

type emailData struct {
	Product string
	Title   string
	Link    string
	MN, EN  section
}

// Verification renders the email-address confirmation message
// (link: /verify-email?token=…).
func (t Templates) Verification(name, token string, ttl time.Duration) (Message, error) {
	p := t.product()
	d := emailData{
		Product: p,
		Title:   p + " — И-мэйл хаягаа баталгаажуулна уу / Verify your email",
		Link:    t.Link("/verify-email", token),
		MN: section{
			Greeting: greetingMN(name),
			Lines:    []string{p + "-д бүртгүүлсэнд баярлалаа. И-мэйл хаягаа баталгаажуулахын тулд доорх товч дээр дарна уу."},
			Button:   "И-мэйл баталгаажуулах",
			Footer:   []string{"Холбоос " + durationMN(ttl) + " хүчинтэй.", "Хэрэв та бүртгүүлээгүй бол энэ захидлыг үл тоомсорлоно уу."},
		},
		EN: section{
			Greeting: greetingEN(name),
			Lines:    []string{"Thanks for signing up for " + p + ". Please confirm your email address."},
			Button:   "Verify email",
			Footer:   []string{"This link expires in " + durationEN(ttl) + ".", "If you did not sign up, you can ignore this email."},
		},
	}
	return render(d)
}

// Invitation renders the team invitation (link: /accept-invitation?token=…).
func (t Templates) Invitation(orgName, inviterName, role, token string, ttl time.Duration) (Message, error) {
	p := t.product()
	inviterMN, inviterEN := "Таныг", "You have"
	if inviterName != "" {
		inviterMN, inviterEN = inviterName+" таныг", inviterName+" has"
	}
	d := emailData{
		Product: p,
		Title:   p + " — " + orgName + " байгууллагад урилга / Invitation to " + orgName,
		Link:    t.Link("/accept-invitation", token),
		MN: section{
			Greeting: "Сайн байна уу!",
			Lines: []string{
				fmt.Sprintf("%s %s дээрх «%s» байгууллагад %s эрхтэйгээр урьж байна.", inviterMN, p, orgName, roleMN(role)),
				"Урилгыг хүлээн авч нууц үгээ тохируулахын тулд доорх товч дээр дарна уу.",
			},
			Button: "Урилга хүлээн авах",
			Footer: []string{"Урилга " + durationMN(ttl) + " хүчинтэй."},
		},
		EN: section{
			Greeting: "Hello!",
			Lines: []string{
				fmt.Sprintf("%s invited you to join “%s” on %s as %s.", inviterEN, orgName, p, roleEN(role)),
				"Accept the invitation and set your password with the button below.",
			},
			Button: "Accept invitation",
			Footer: []string{"This invitation expires in " + durationEN(ttl) + "."},
		},
	}
	return render(d)
}

// PasswordReset renders the reset message (link: /reset-password?token=…).
func (t Templates) PasswordReset(name, token string, ttl time.Duration) (Message, error) {
	p := t.product()
	d := emailData{
		Product: p,
		Title:   p + " — Нууц үг сэргээх / Reset your password",
		Link:    t.Link("/reset-password", token),
		MN: section{
			Greeting: greetingMN(name),
			Lines:    []string{"Таны бүртгэлийн нууц үгийг сэргээх хүсэлт ирлээ. Шинэ нууц үг тохируулахын тулд доорх товч дээр дарна уу."},
			Button:   "Нууц үг сэргээх",
			Footer:   []string{"Холбоос " + durationMN(ttl) + " хүчинтэй бөгөөд нэг удаа ашиглагдана.", "Хэрэв та хүсэлт илгээгээгүй бол энэ захидлыг үл тоомсорлоно уу — таны нууц үг өөрчлөгдөхгүй."},
		},
		EN: section{
			Greeting: greetingEN(name),
			Lines:    []string{"We received a request to reset your password. Choose a new password with the button below."},
			Button:   "Reset password",
			Footer:   []string{"This single-use link expires in " + durationEN(ttl) + ".", "If you did not request this, ignore this email — your password will not change."},
		},
	}
	return render(d)
}

func greetingMN(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return "Сайн байна уу, " + name + "!"
	}
	return "Сайн байна уу!"
}

func greetingEN(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return "Hello " + name + ","
	}
	return "Hello,"
}

func roleMN(role string) string {
	switch role {
	case "owner":
		return "эзэмшигч"
	case "admin":
		return "админ"
	default:
		return "оператор"
	}
}

func roleEN(role string) string {
	switch role {
	case "owner":
		return "an owner"
	case "admin":
		return "an admin"
	default:
		return "an operator"
	}
}

func durationMN(d time.Duration) string {
	switch {
	case d <= 0:
		return "хязгаарлагдмал хугацаанд"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d хоногийн турш", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d цагийн турш", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d минутын турш", int(d.Round(time.Minute)/time.Minute))
	}
}

func durationEN(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d <= 0:
		return "a limited time"
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d.Round(time.Minute)/time.Minute), "minute")
	}
}

var textTpl = texttemplate.Must(texttemplate.New("text").Parse(`{{with .MN}}{{.Greeting}}

{{range .Lines}}{{.}}
{{end}}{{end}}
{{.Link}}

{{range .MN.Footer}}{{.}}
{{end}}
----------------------------------------

{{with .EN}}{{.Greeting}}

{{range .Lines}}{{.}}
{{end}}{{end}}
{{.Link}}

{{range .EN.Footer}}{{.}}
{{end}}
— {{.Product}}
`))

// "list" builds the two language sections for the range in htmlTpl.
var htmlTpl = htmltemplate.Must(htmltemplate.New("html").Funcs(htmltemplate.FuncMap{
	"list": func(s ...section) []section { return s },
}).Parse(`<!doctype html>
<html lang="mn"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title></head>
<body style="margin:0;padding:0;background:#f4f4f5;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#18181b">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f4f4f5;padding:24px 0"><tr><td align="center">
<table role="presentation" width="560" cellspacing="0" cellpadding="0" style="max-width:560px;width:100%;background:#ffffff;border-radius:8px;border:1px solid #e4e4e7">
<tr><td style="padding:24px 32px;border-bottom:1px solid #e4e4e7;font-size:18px;font-weight:600">{{.Product}}</td></tr>
{{range $i, $s := (list .MN .EN)}}
<tr><td style="padding:24px 32px{{if $i}};border-top:1px solid #e4e4e7{{end}}">
<p style="margin:0 0 12px;font-size:16px;font-weight:600">{{$s.Greeting}}</p>
{{range $s.Lines}}<p style="margin:0 0 12px;font-size:14px;line-height:1.5">{{.}}</p>{{end}}
<p style="margin:20px 0"><a href="{{$.Link}}" style="display:inline-block;background:#18181b;color:#ffffff;text-decoration:none;padding:10px 20px;border-radius:6px;font-size:14px;font-weight:600">{{$s.Button}}</a></p>
{{range $s.Footer}}<p style="margin:0 0 6px;font-size:12px;color:#71717a;line-height:1.5">{{.}}</p>{{end}}
</td></tr>
{{end}}
<tr><td style="padding:16px 32px;border-top:1px solid #e4e4e7;font-size:12px;color:#71717a;word-break:break-all">{{.Link}}</td></tr>
</table></td></tr></table>
</body></html>
`))

func render(d emailData) (Message, error) {
	var tb, hb bytes.Buffer
	if err := textTpl.Execute(&tb, d); err != nil {
		return Message{}, fmt.Errorf("render text email: %w", err)
	}
	if err := htmlTpl.Execute(&hb, d); err != nil {
		return Message{}, fmt.Errorf("render html email: %w", err)
	}
	return Message{Subject: d.Title, Text: strings.TrimSpace(tb.String()) + "\n", HTML: hb.String()}, nil
}
