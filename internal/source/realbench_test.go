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
