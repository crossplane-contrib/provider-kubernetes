package aws

import (
	"testing"
)

func Test_extractClusterNameFromARN(t *testing.T) {
	tests := []struct {
		name    string
		arn     string
		want    string
		wantErr bool
	}{
		{
			name:    "valid EKS cluster ARN",
			arn:     "arn:aws:eks:eu-central-1:609897127049:cluster/configuration-aws-lb-controller-dc7jw",
			want:    "configuration-aws-lb-controller-dc7jw",
			wantErr: false,
		},
		{
			name:    "valid EKS cluster ARN with different region",
			arn:     "arn:aws:eks:us-west-2:123456789012:cluster/my-cluster",
			want:    "my-cluster",
			wantErr: false,
		},
		{
			name:    "plain cluster name (not an ARN)",
			arn:     "my-cluster-name",
			want:    "my-cluster-name",
			wantErr: false,
		},
		{
			name:    "invalid ARN format - missing cluster name",
			arn:     "arn:aws:eks:us-west-2:123456789012:cluster",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid ARN - not EKS",
			arn:     "arn:aws:s3:::my-bucket",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty string",
			arn:     "",
			want:    "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractClusterNameFromARN(tt.arn)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractClusterNameFromARN() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractClusterNameFromARN() = %v, want %v", got, tt.want)
			}
		})
	}
}
