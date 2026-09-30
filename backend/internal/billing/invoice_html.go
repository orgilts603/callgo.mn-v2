package billing

import (
	"bytes"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// InvoiceHTML renders a printable, self-contained HTML invoice with
// Mongolian labels.
//
// docs/API.md names the route .../pdf and application/pdf, but the standard
// PDF fonts have no Cyrillic glyphs and generating a PDF with an embedded
// font needs a dependency we do not ship; the endpoint therefore serves this
// document as text/html (inline) and the browser's "Print → Save as PDF"
// produces the PDF.
func InvoiceHTML(inv *domain.Invoice, org *domain.Organization) string {
	tpl, err := template.New("invoice").Funcs(template.FuncMap{
		"mnt":    FormatMNT,
		"date":   func(t time.Time) string { return t.Format("2006-01-02") },
		"qty":    formatQty,
		"status": invoiceStatusLabel,
	}).Parse(invoiceTemplate)
	if err != nil {
		return "<!doctype html><title>invoice</title><p>invoice template error</p>"
	}
	orgName := ""
	if org != nil {
		orgName = org.Name
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{"Inv": inv, "OrgName": orgName, "OrgID": inv.OrgID.String()}); err != nil {
		return "<!doctype html><title>invoice</title><p>invoice render error</p>"
	}
	return buf.String()
}

// FormatMNT formats an amount as "1 234 567 ₮".
func FormatMNT(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	out := b.String() + " ₮"
	if neg {
		out = "-" + out
	}
	return out
}

func formatQty(q float64) string {
	return strconv.FormatFloat(q, 'f', -1, 64)
}

func invoiceStatusLabel(s domain.InvoiceStatus) string {
	switch s {
	case domain.InvoiceOpen:
		return "Төлөгдөөгүй"
	case domain.InvoicePaid:
		return "Төлөгдсөн"
	case domain.InvoiceVoid:
		return "Хүчингүй"
	case domain.InvoiceDraft:
		return "Ноорог"
	}
	return string(s)
}

const invoiceTemplate = `<!doctype html>
<html lang="mn">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Нэхэмжлэх {{.Inv.Number}}</title>
<style>
  :root { color-scheme: light; }
  body { font-family: "Segoe UI", Roboto, Arial, sans-serif; color: #1a1a1a; background: #fff; margin: 0; }
  .page { max-width: 780px; margin: 32px auto; padding: 0 24px; }
  header { display: flex; justify-content: space-between; align-items: flex-start; border-bottom: 2px solid #1a1a1a; padding-bottom: 16px; }
  h1 { font-size: 26px; margin: 0 0 4px; }
  .muted { color: #666; font-size: 13px; }
  .status { display: inline-block; padding: 4px 10px; border-radius: 4px; font-weight: 600; font-size: 13px; background: #eee; }
  .status.paid { background: #e3f5e8; color: #17672e; }
  .status.open { background: #fff4db; color: #8a5a00; }
  .parties { display: flex; justify-content: space-between; gap: 24px; margin: 24px 0; }
  .parties div { flex: 1; }
  .label { font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: #666; margin-bottom: 4px; }
  table { width: 100%; border-collapse: collapse; margin-top: 8px; }
  th, td { padding: 10px 8px; border-bottom: 1px solid #ddd; text-align: left; font-size: 14px; vertical-align: top; }
  th { font-size: 12px; text-transform: uppercase; color: #666; }
  td.num, th.num { text-align: right; white-space: nowrap; }
  .totals { margin-left: auto; width: 320px; margin-top: 16px; }
  .totals td { border: none; padding: 6px 8px; }
  .totals tr.grand td { border-top: 2px solid #1a1a1a; font-weight: 700; font-size: 16px; }
  footer { margin-top: 40px; font-size: 12px; color: #666; }
  @media print { .page { margin: 0; } .noprint { display: none; } }
</style>
</head>
<body>
<div class="page">
  <header>
    <div>
      <h1>НЭХЭМЖЛЭХ</h1>
      <div class="muted">Дугаар: <strong>{{.Inv.Number}}</strong></div>
      <div class="muted">Огноо: {{date .Inv.CreatedAt}}</div>
      <div class="muted">Төлөх хугацаа: {{date .Inv.DueAt}}</div>
    </div>
    <div><span class="status {{.Inv.Status}}">{{status .Inv.Status}}</span></div>
  </header>

  <div class="parties">
    <div>
      <div class="label">Нэхэмжлэгч</div>
      <strong>CallGo.mn</strong><br>
      <span class="muted">AI дуудлагын төв — SaaS үйлчилгээ</span>
    </div>
    <div>
      <div class="label">Төлөгч</div>
      <strong>{{.OrgName}}</strong><br>
      <span class="muted">Байгууллагын ID: {{.OrgID}}</span>
    </div>
    <div>
      <div class="label">Хамрах хугацаа</div>
      {{date .Inv.PeriodStart}} – {{date .Inv.PeriodEnd}}
    </div>
  </div>

  <table>
    <thead>
      <tr><th>Утга</th><th class="num">Тоо хэмжээ</th><th class="num">Нэгж үнэ</th><th class="num">Дүн</th></tr>
    </thead>
    <tbody>
      {{range .Inv.Lines}}
      <tr><td>{{.Description}}</td><td class="num">{{qty .Quantity}}</td><td class="num">{{mnt .UnitMNT}}</td><td class="num">{{mnt .AmountMNT}}</td></tr>
      {{end}}
    </tbody>
  </table>

  <table class="totals">
    <tr><td>Дүн (НӨАТ-гүй)</td><td class="num">{{mnt .Inv.SubtotalMNT}}</td></tr>
    <tr><td>НӨАТ</td><td class="num">{{mnt .Inv.VATMNT}}</td></tr>
    <tr class="grand"><td>Нийт төлөх</td><td class="num">{{mnt .Inv.TotalMNT}}</td></tr>
    {{if .Inv.PaidAt}}<tr><td>Төлсөн огноо</td><td class="num">{{date .Inv.PaidAt}}</td></tr>{{end}}
  </table>

  <footer>
    Төлбөрийг QPay-ээр эсвэл дансаар шилжүүлэх боломжтой. Гүйлгээний утга дээр нэхэмжлэхийн дугаарыг ({{.Inv.Number}}) бичнэ үү.
    <p class="noprint">PDF хэлбэрээр хадгалахын тулд хөтчийн «Хэвлэх → PDF болгон хадгалах» сонголтыг ашиглана уу.</p>
  </footer>
</div>
</body>
</html>
`
