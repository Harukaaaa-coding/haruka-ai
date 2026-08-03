package file

import (
	"GopherAI/service/knowledgebase"
	"context"
	"mime/multipart"
)

// UploadRagFile preserves the original synchronous upload contract. The file
// is now stored as a document in the user's default knowledge base, so existing
// clients automatically benefit from multi-document storage and chunking.
func UploadRagFile(username string, file *multipart.FileHeader) (string, error) {
	return knowledgebase.UploadLegacy(context.Background(), username, file)
}
