// Package pdfexport genera el PDF de la lista de candidatos de una solicitud.
//
// Incluye únicamente los datos públicos que la empresa puede ver (sin teléfono,
// correo ni dirección), respetando el requerimiento de privacidad.
package pdfexport

import (
	"bytes"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/go-pdf/fpdf"
)

// CandidateList construye un PDF con la solicitud y sus candidatos públicos.
func CandidateList(req *models.Request, cands []models.CandidatePublic) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()

	// Encabezado
	pdf.SetFont("Helvetica", "B", 22)
	pdf.SetTextColor(13, 27, 62) // navy neXus
	pdf.CellFormat(0, 12, "neXus  -  Lista de candidatos", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(0, 6, "Conectando talento con oportunidades.", "", 1, "L", false, 0, "")
	pdf.Ln(4)

	// Datos de la solicitud
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(0, 8, tr(pdf, "Solicitud "+req.Folio), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	line(pdf, "Folio", req.Folio)
	line(pdf, "Tipo de trabajador", req.TipoTrabajador)
	line(pdf, "Cantidad", itoa(req.CantidadTrabajadores))
	line(pdf, "Zona", req.CiudadZona)
	line(pdf, "Inicio", req.FechaInicio+" "+req.HoraInicio)
	line(pdf, "Duracion", req.DuracionEstimada)
	line(pdf, "Estado", req.Estado)
	pdf.Ln(4)

	// Tabla de candidatos
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(0, 8, tr(pdf, "Candidatos ("+itoa(len(cands))+")"), "", 1, "L", false, 0, "")
	pdf.Ln(1)

	header := []string{"Nombre", "Oficio", "Ciudad", "Exp.", "Estado"}
	widths := []float64{55, 45, 35, 15, 30}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetFillColor(13, 27, 62)
	pdf.SetTextColor(255, 255, 255)
	for i, h := range header {
		pdf.CellFormat(widths[i], 8, tr(pdf, h), "1", 0, "L", true, 0, "")
	}
	pdf.Ln(-1)

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(0, 0, 0)
	fill := false
	for _, c := range cands {
		if fill {
			pdf.SetFillColor(245, 247, 251)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		row := []string{c.Nombre, c.Oficio, c.Ciudad, itoa(c.Experiencia), c.Estado}
		for i, v := range row {
			pdf.CellFormat(widths[i], 8, tr(pdf, v), "1", 0, "L", true, 0, "")
		}
		pdf.Ln(-1)
		fill = !fill
	}

	// Pie con aviso legal
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.SetTextColor(120, 120, 120)
	pdf.MultiCell(0, 4, tr(pdf, models.DisclaimerLegal), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tr traduce a la codificación interna de fpdf (Latin-1) para acentos.
func tr(pdf *fpdf.Fpdf, s string) string {
	return pdf.UnicodeTranslatorFromDescriptor("")(s)
}

func line(pdf *fpdf.Fpdf, k, v string) {
	pdf.SetFont("Helvetica", "B", 11)
	pdf.CellFormat(45, 6, tr(pdf, k), "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	pdf.CellFormat(0, 6, tr(pdf, v), "", 1, "L", false, 0, "")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
