package source

import (
	"context"
	"os"
	"testing"
)

// realBenchSource opens the real export of ONEC_REAL_DUMP for a benchmark, or
// skips: the numbers only mean something on a real configuration.
func realBenchSource(b *testing.B) *XMLSource {
	b.Helper()
	root := os.Getenv("ONEC_REAL_DUMP")
	if root == "" {
		b.Skip("set ONEC_REAL_DUMP to a 1C XML export directory to run this")
	}
	return NewXMLSource(root)
}

// BenchmarkRealExtensionPoints reads every overridable module of the export
// once per call, which is what bsp_extension_points costs.
func BenchmarkRealExtensionPoints(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.ExtensionPoints(context.Background(), "заполнение документа", 100); err != nil {
			b.Fatal(err)
		}
	}
}

// realBenchDocuments are documents of ut_demo with a large object module:
// delegated posting and inline posting both.
var realBenchDocuments = []string{
	"ПриобретениеТоваровУслуг", "КорректировкаПриобретения",
	"РаспределениеДоходовПоНаправлениямДеятельности", "Анкета",
	"КорректировкаНалогообложенияНДСПартийТоваров",
}

// BenchmarkRealPostingReview reviews the posting of several documents per
// iteration, as get_movements review=true does for each of them.
func BenchmarkRealPostingReview(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, doc := range realBenchDocuments {
			if _, err := s.PostingReview(context.Background(), doc); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// realBenchExchangeObjects are ut_demo objects in exchange plans whose write
// path carries registration subscriptions into large БСП common modules.
var realBenchExchangeObjects = [][2]string{
	{"Catalog", "Номенклатура"}, {"Catalog", "Контрагенты"}, {"Catalog", "Партнеры"},
	{"Document", "ЗаказКлиента"}, {"Document", "РеализацияТоваровУслуг"},
}

// BenchmarkRealExchangeAudit audits several objects per iteration, each call
// reading the handler modules of its subscriptions once.
func BenchmarkRealExchangeAudit(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, o := range realBenchExchangeObjects {
			if _, err := s.ExchangeAudit(context.Background(), o[0], o[1]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRealWritePath builds the write path of the same objects: its
// subscription handlers are read from the same common modules.
func BenchmarkRealWritePath(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, o := range realBenchExchangeObjects {
			if _, err := s.WritePath(context.Background(), o[0], o[1]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// realBenchForms are large form modules of ut_demo.
var realBenchForms = [][3]string{
	{"Document", "ЗаказКлиента", "ФормаДокумента"},
	{"Document", "РеализацияТоваровУслуг", "ФормаДокумента"},
	{"Document", "ПриобретениеТоваровУслуг", "ФормаДокумента"},
	{"Catalog", "Номенклатура", "ФормаЭлемента"},
	{"Catalog", "Партнеры", "ФормаЭлемента"},
}

// BenchmarkRealFormImpact analyses several forms per iteration, one form
// module read and cut into procedures per call.
func BenchmarkRealFormImpact(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, f := range realBenchForms {
			if _, err := s.FormImpact(context.Background(), f[0], f[1], f[2], nil, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRealAccessProfiles reads the profiles supplied by the code, as
// rights_audit does for a profile.
func BenchmarkRealAccessProfiles(b *testing.B) {
	s := realBenchSource(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.AccessProfiles(); err != nil {
			b.Fatal(err)
		}
	}
}
