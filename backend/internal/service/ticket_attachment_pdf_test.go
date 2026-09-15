package service

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 测试夹具由内存生成真实交叉引用，不依赖外部文件、解析工具或危险可执行载荷。
func ticketTestPDF() []byte {
	return ticketTestPDFObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources << >> >>",
	}, "")
}

func ticketTestPDFObjects(objects []string, trailer string) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R %s >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), trailer, xref)
	return out.Bytes()
}

func ticketTestPDFFlate(data []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	_, _ = w.Write(data)
	_ = w.Close()
	return out.Bytes()
}

// 同时覆盖 PDF 1.5 起常见的压缩对象流与二进制交叉引用流。
func ticketTestModernPDF(extra string, padding int, declaredSize int) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R " + extra + " >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Resources << >> /Note (" + strings.Repeat("a", padding) + ") >>",
	}
	var header, content bytes.Buffer
	for i, object := range objects {
		fmt.Fprintf(&header, "%d %d ", i+1, content.Len())
		content.WriteString(object + "\n")
	}
	plain := append(append([]byte{}, header.Bytes()...), content.Bytes()...)
	compressed := ticketTestPDFFlate(plain)
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	streamOffset := out.Len()
	fmt.Fprintf(&out, "4 0 obj\n<< /Type /ObjStm /N 3 /First %d /Filter /FlateDecode /Length %d >>\nstream\n", header.Len(), len(compressed))
	out.Write(compressed)
	out.WriteString("\nendstream\nendobj\n")
	xrefOffset := out.Len()
	entries := make([]byte, 6*7)
	for i := 0; i < 6; i++ {
		entry := entries[i*7 : (i+1)*7]
		switch {
		case i == 0:
			binary.BigEndian.PutUint16(entry[5:], 65535)
		case i <= 3:
			entry[0] = 2
			binary.BigEndian.PutUint32(entry[1:5], 4)
			binary.BigEndian.PutUint16(entry[5:], uint16(i-1))
		case i == 4:
			entry[0] = 1
			binary.BigEndian.PutUint32(entry[1:5], uint32(streamOffset))
		case i == 5:
			entry[0] = 1
			binary.BigEndian.PutUint32(entry[1:5], uint32(xrefOffset))
		}
	}
	compressedXref := ticketTestPDFFlate(entries)
	fmt.Fprintf(&out, "5 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 2] /Filter /FlateDecode /Length %d >>\nstream\n", declaredSize, len(compressedXref))
	out.Write(compressedXref)
	fmt.Fprintf(&out, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return out.Bytes()
}

func TestTicketPDFAcceptsRealAndModernDocuments(t *testing.T) {
	require.NoError(t, validateTicketPDF(ticketTestPDF()))
	require.NoError(t, validateTicketPDF(ticketTestModernPDF("", 0, 6)))
	require.NoError(t, validateTicketPDF(ticketTestModernPDF("/Description (JavaScript is a topic in this report)", 0, 6)))
}

func TestTicketPDFRejectsActiveContent(t *testing.T) {
	for name, extra := range map[string]string{
		"javascript":         "/OpenAction << /S /JavaScript /JS (void 0) >>",
		"escaped_names":      "/Names << /Java#53cript << /Names [(example) << /S /JavaScript /J#53 (void 0) >>] >> >>",
		"automatic_action":   "/AA << /WC << /S /GoTo /D [3 0 R /Fit] >> >>",
		"embedded_file":      "/Names << /EmbeddedFiles << /Names [] >> >>",
		"launch":             "/Action << /S /Launch /F (example.txt) >>",
		"external_program":   "/Action << /S /URI /URI (file:///example.txt) >>",
		"script_uri":         "/Action << /S /URI /URI (javascript:void%200) >>",
		"xfa":                "/AcroForm << /XFA (example) >>",
		"associated_content": "/AF []",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateTicketPDF(ticketTestPDFObjects([]string{
				"<< /Type /Catalog /Pages 2 0 R " + extra + " >>",
				"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
			}, "")))
			require.Error(t, validateTicketPDF(ticketTestModernPDF(extra, 0, 6)))
		})
	}
	// 正常网页链接保留，检查过程本身不会发起请求。
	require.NoError(t, validateTicketPDF(ticketTestModernPDF("/Action << /S /URI /URI (https://example.com/support) >>", 0, 6)))
}

func TestTicketPDFRejectsMalformedAndEncryptedDocuments(t *testing.T) {
	require.Error(t, validateTicketPDF([]byte("%PDF-1.7\nnot a document\n%%EOF")))
	require.Error(t, validateTicketPDF(append(ticketTestPDF(), []byte("trailing payload")...)))
	require.Error(t, validateTicketPDF(ticketTestPDFObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [2 0 R] >>",
	}, "")))
	require.Error(t, validateTicketPDF(ticketTestPDFObjects([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R >>",
		"<< /Filter /Standard /V 1 /R 2 /O () /U () /P -4 >>",
	}, "/Encrypt 4 0 R")))
}

func TestTicketPDFEnforcesResourceBudgets(t *testing.T) {
	t.Run("compressed_object_expansion", func(t *testing.T) {
		data := ticketTestModernPDF("", ticketPDFMaxDecodedBytes+1024, 6)
		require.Less(t, len(data), 32<<10)
		require.Error(t, validateTicketPDF(data))
	})
	t.Run("xref_object_count", func(t *testing.T) {
		require.Error(t, validateTicketPDF(ticketTestModernPDF("", 0, ticketPDFMaxObjects+1)))
	})
	t.Run("classic_xref_object_count", func(t *testing.T) {
		for _, newline := range []string{"\n", "\r", "\r\n"} {
			data := bytes.Repeat([]byte("0000000000 00000 n"+newline), ticketPDFMaxObjects+1)
			require.Error(t, ticketPDFXRefBudget(data))
		}
	})
	t.Run("total_object_stream_expansion", func(t *testing.T) {
		data := ticketTestManyPDFObjectStreams(5, 7<<20)
		require.Less(t, len(data), 128<<10)
		require.ErrorContains(t, validateTicketPDF(data), "解压大小")
	})
	t.Run("recursive_direct_objects", func(t *testing.T) {
		extra := "/Example " + strings.Repeat("[", ticketPDFMaxDepth+10) + "null" + strings.Repeat("]", ticketPDFMaxDepth+10)
		require.Error(t, validateTicketPDF(ticketTestModernPDF(extra, 0, 6)))
	})
	t.Run("busy_is_retryable", func(t *testing.T) {
		ticketPDFSlots <- struct{}{}
		defer func() { <-ticketPDFSlots }()
		require.ErrorIs(t, validateTicketPDF(ticketTestPDF()), ErrTicketAttachmentBusy)
	})
}

// 多个单流均合法但合计过大的压缩对象不能绕过整份附件的解压预算。
func ticketTestManyPDFObjectStreams(streamCount, padding int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	xrefID := 4 + streamCount*2
	entries := make([]byte, (xrefID+1)*7)
	binary.BigEndian.PutUint16(entries[5:7], 65535)
	mark := func(id int) {
		entry := entries[id*7 : (id+1)*7]
		entry[0] = 1
		binary.BigEndian.PutUint32(entry[1:5], uint32(out.Len()))
	}
	for i, object := range []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	} {
		mark(i + 1)
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	for i := 0; i < streamCount; i++ {
		streamID, objectID := 4+i, 4+streamCount+i
		mark(streamID)
		entry := entries[objectID*7 : (objectID+1)*7]
		entry[0] = 2
		binary.BigEndian.PutUint32(entry[1:5], uint32(streamID))
		header := fmt.Sprintf("%d 0 ", objectID)
		plain := header + "(" + strings.Repeat("a", padding) + ")"
		encoded := ticketTestPDFFlate([]byte(plain))
		fmt.Fprintf(&out, "%d 0 obj\n<< /Type /ObjStm /N 1 /First %d /Filter /FlateDecode /Length %d >>\nstream\n", streamID, len(header), len(encoded))
		out.Write(encoded)
		out.WriteString("\nendstream\nendobj\n")
	}
	xref := out.Len()
	mark(xrefID)
	encoded := ticketTestPDFFlate(entries)
	fmt.Fprintf(&out, "%d 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 2] /Filter /FlateDecode /Length %d >>\nstream\n", xrefID, xrefID+1, len(encoded))
	out.Write(encoded)
	fmt.Fprintf(&out, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes()
}
