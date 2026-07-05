//go:build !credaws

// This stub compiles when the `credaws` build tag is absent. It registers the
// "aws-sts" name so Open reports an actionable "built without credaws" error
// instead of an "unknown backend" one, mirroring pkg/storage's cloudblob gate.
package awssts

import (
	"fmt"

	"cornus/pkg/credential"
)

func init() {
	credential.Register("aws-sts", func(map[string]string) (credential.Source, error) {
		return nil, fmt.Errorf("aws-sts: this cornus binary was built without the AWS backend (rebuild with -tags credaws)")
	})
}
