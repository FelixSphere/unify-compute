package artifacts

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
)

// The env the run step reads. backend refuses customer env names with this
// prefix, so these cannot be shadowed.
const (
	EnvURL   = backend.ReservedEnvPrefix + "ARTIFACTS_URL"
	EnvToken = backend.ReservedEnvPrefix + "ARTIFACTS_TOKEN"
)

// UploadMargin is how long an upload token outlives max_runtime_seconds,
// counted from launch: provisioning comes before the run and the upload
// after the command. A job that ends at its cap is torn down by the backend
// and does not get to upload anyway.
const UploadMargin = 2 * time.Hour

// MaxFiles is how many files one job's outputs may have. Each costs a
// callback and an upload from the node and a HEAD at completion.
const MaxFiles = 10000

var (
	ErrNoOwner      = errors.New("artifacts: job has no owner to bind the upload to")
	ErrMountOverlap = errors.New("artifacts: output mounts must not be / or contain one another")
)

// OutputMounts validates spec.outputs and returns the mounts, cleaned.
func OutputMounts(outs []backend.Output) ([]string, error) {
	ms := make([]string, 0, len(outs))
	for _, o := range outs {
		if !path.IsAbs(o.Mount) {
			return nil, fmt.Errorf("%w: %q", backend.ErrBadMount, o.Mount)
		}
		m := path.Clean(o.Mount)
		if m == "/" {
			return nil, fmt.Errorf("%w: %q", ErrMountOverlap, o.Mount)
		}
		for _, prev := range ms {
			if m == prev || strings.HasPrefix(m, prev+"/") || strings.HasPrefix(prev, m+"/") {
				return nil, fmt.Errorf("%w: %q and %q", ErrMountOverlap, prev, m)
			}
		}
		ms = append(ms, m)
	}
	return ms, nil
}

// OutputStep implements backend.OutputUploader. The node needs bash, curl,
// find and sha256sum, and nothing else: SkyPilot installs curl into every
// container it sets up (sky/provision/docker_utils.py:431 on VMs,
// sky/templates/kubernetes-ray.yml.j2:936 on Kubernetes, both at v0.13.0)
// and the rest are coreutils/findutils, present in any Debian-based image.
// It holds no AWS credentials: each file gets its own presigned PUT from
// compute-api, so there is no AWS CLI or SDK to install and no credential
// lifetime to outlast the job.
func (s *Service) OutputStep(j backend.Job, command string) (map[string]string, string, error) {
	mounts, err := OutputMounts(j.Spec.Outputs)
	if err != nil {
		return nil, "", err
	}
	if j.Owner == "" {
		return nil, "", ErrNoOwner
	}
	if !jobIDPattern.MatchString(j.ID) {
		return nil, "", fmt.Errorf("artifacts: job id %q", j.ID)
	}
	exp := s.now().Add(time.Duration(j.Spec.MaxRuntimeSeconds)*time.Second + UploadMargin)
	envs := map[string]string{
		EnvURL:   strings.TrimRight(s.cfg.PublicURL, "/") + "/compute/v1/internal/jobs/" + j.ID + "/artifacts",
		EnvToken: mintToken(s.cfg.TokenSecret, j.Owner, j.ID, exp),
	}
	return envs, runScript(mounts, command), nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

// runScript runs command, then uploads every regular file under the mounts
// from an EXIT trap, so a failed command still gets its outputs uploaded and
// the step still exits with the command's status. Symlinks are not followed.
func runScript(mounts []string, command string) string {
	q := make([]string, len(mounts))
	for i, m := range mounts {
		q[i] = shq(m)
	}
	list := strings.Join(q, " ")
	return fmt.Sprintf(`mkdir -p -- %[1]s
__uc_upload() {
  __uc_rc=$?
  trap - EXIT
  if ! command -v curl >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
    echo "unify-compute: curl and sha256sum are needed to upload outputs; nothing was uploaded" >&2
    exit "$__uc_rc"
  fi
  __uc_auth="Authorization: Bearer $%[2]s"
  __uc_n=0
  __uc_bad=0
  for __uc_m in %[1]s; do
    [ -d "$__uc_m" ] || continue
    while IFS= read -r -d '' __uc_f; do
      __uc_n=$((__uc_n + 1))
      if [ "$__uc_n" -gt %[4]d ]; then
        echo "unify-compute: more than %[4]d output files; the rest were not uploaded" >&2
        __uc_n=%[4]d
        __uc_bad=1
        break 2
      fi
      __uc_size=$(($(wc -c <"$__uc_f")))
      __uc_sha=$(sha256sum <"$__uc_f" | cut -d' ' -f1)
      if ! __uc_resp=$(curl -fsS --retry 3 -X POST -H "$__uc_auth" --data-urlencode "path=$__uc_f" \
          --data-urlencode "size=$__uc_size" --data-urlencode "sha256=$__uc_sha" "$%[3]s/uploads"); then
        echo "unify-compute: no upload URL for $__uc_f" >&2
        __uc_bad=1
        continue
      fi
      read -r __uc_sum __uc_put <<EOF
$__uc_resp
EOF
      if ! curl -fsS --retry 3 -o /dev/null -T "$__uc_f" -H "x-amz-checksum-sha256: $__uc_sum" "$__uc_put"; then
        echo "unify-compute: upload of $__uc_f failed" >&2
        __uc_bad=1
      fi
    done < <(find "$__uc_m" -type f -print0)
  done
  if curl -fsS --retry 3 -X POST -H "$__uc_auth" -o /dev/null "$%[3]s/complete"; then
    echo "unify-compute: $__uc_n output file(s) processed, manifest written$([ "$__uc_bad" = 0 ] || echo '; some failed, see above')" >&2
  else
    echo "unify-compute: the output manifest could not be written" >&2
  fi
  exit "$__uc_rc"
}
trap __uc_upload EXIT
%[5]s
`, list, EnvToken, EnvURL, MaxFiles, command)
}
