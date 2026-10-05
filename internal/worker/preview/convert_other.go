//go:build !windows

package preview

import "context"

// ConvertToPDF is Windows-only (Office COM automation); elsewhere it reports
// the document as not convertible.
func ConvertToPDF(ctx context.Context, srcPath, dstPath string) (bool, error) {
	return false, nil
}
