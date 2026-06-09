#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
	echo "usage: $0 <version> [output-dir]" >&2
	exit 2
fi

version="${1#v}"
out_dir="${2:-dist/packages}"
package_name="birdwatcher"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_dir="${root_dir}/dist/package-build"

if ! command -v fpm >/dev/null 2>&1; then
	echo "fpm is required to build release packages" >&2
	exit 1
fi

rm -rf "${build_dir}" "${out_dir}"
mkdir -p "${build_dir}" "${out_dir}"

build_rootfs() {
	local goarch="$1"
	local rootfs="${build_dir}/${goarch}/rootfs"

	rm -rf "${rootfs}"
	mkdir -p "${rootfs}/opt/birdwatcher/birdwatcher/bin"
	mkdir -p "${rootfs}/etc/birdwatcher"
	mkdir -p "${rootfs}/usr/lib/systemd/system"

	CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
		go build \
		-ldflags "-X main.VERSION=${version}" \
		-o "${rootfs}/opt/birdwatcher/birdwatcher/bin/birdwatcher" \
		"${root_dir}"

	cp "${root_dir}/install/systemd/"*.service "${rootfs}/usr/lib/systemd/system/"
	cp "${root_dir}/etc/birdwatcher/"* "${rootfs}/etc/birdwatcher/"

	echo "${rootfs}"
}

build_packages() {
	local goarch="$1"
	local deb_arch="$2"
	local rpm_arch="$3"
	local rootfs

	rootfs="$(build_rootfs "${goarch}")"

	(
		cd "${out_dir}"
		fpm \
			-s dir \
			-t deb \
			-n "${package_name}" \
			-v "${version}" \
			--iteration 1 \
			--architecture "${deb_arch}" \
			--maintainer "birdwatcher maintainers" \
			--license "BSD-3-Clause" \
			--url "https://github.com/alice-lg/birdwatcher" \
			--description "HTTP API and Prometheus exporter for the BIRD routing daemon" \
			--config-files /etc/birdwatcher/birdwatcher.conf \
			-C "${rootfs}" \
			opt etc usr

		fpm \
			-s dir \
			-t rpm \
			-n "${package_name}" \
			-v "${version}" \
			--iteration 1 \
			--architecture "${rpm_arch}" \
			--rpm-os linux \
			--maintainer "birdwatcher maintainers" \
			--license "BSD-3-Clause" \
			--url "https://github.com/alice-lg/birdwatcher" \
			--description "HTTP API and Prometheus exporter for the BIRD routing daemon" \
			--config-files /etc/birdwatcher/birdwatcher.conf \
			-C "${rootfs}" \
			opt etc usr
	)
}

build_packages amd64 amd64 x86_64
build_packages arm64 arm64 aarch64

(
	cd "${out_dir}"
	sha256sum ./* > SHA256SUMS
)
