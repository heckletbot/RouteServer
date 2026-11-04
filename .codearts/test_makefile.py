#!/usr/bin/env python3
# Copyright 2026 The RouteServer Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Exercise Make test pipelines in temporary directories without running E2E."""

import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import tarfile
import unittest
import zipfile


MAKEFILE = Path(__file__).resolve().parents[1] / "Makefile"
TARGETS = {
    "test": "test.log",
    "test-unit": "unit-test.log",
    "test-coverage": "coverage.log",
}


class MakefileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="routeserver-makefile-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.args_file = self.root / "go-args"
        self.env = os.environ.copy()
        self.env.pop("MAKEFLAGS", None)
        self.env.pop("MFLAGS", None)
        self.env["ROUTESERVER_TEST_ARGS"] = str(self.args_file)
        self.env["PATH"] = str(self.bin_dir) + os.pathsep + self.env["PATH"]
        self.fake_go = self.executable("go-fixture", """#!/bin/sh
printf '%s\\n' "$@" > "$ROUTESERVER_TEST_ARGS"
printf 'fixture stdout\\n'
printf 'fixture stderr\\n' >&2
exit "${ROUTESERVER_TEST_GO_EXIT:-0}"
""")

    def executable(self, name, source):
        path = self.bin_dir / name
        path.write_text(source)
        path.chmod(0o755)
        return path

    def run_target(self, target, go=None, env=None):
        if self.args_file.exists():
            self.args_file.unlink()
        build_dir = self.root / "build" / target
        result = subprocess.run(
            ["make", "--no-print-directory", "-f", str(MAKEFILE), target,
             "GO=" + str(go or self.fake_go), "BUILD_DIR=" + str(build_dir)],
            cwd=self.root, env=env or self.env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120,
        )
        return result, build_dir / TARGETS[target]

    def assert_output_logged(self, result, log):
        for marker in ("fixture stdout", "fixture stderr"):
            self.assertIn(marker, result.stdout)
            self.assertIn(marker, log.read_text())

    def failing_tee(self):
        real_tee = shutil.which("tee")
        self.assertIsNotNone(real_tee)
        self.executable("tee", """#!/bin/sh
if [ "$1" = "-a" ]; then
    cat
    exit 23
fi
exec """ + shlex.quote(real_tee) + " \"$@\"\n")

    def test_success_keeps_logs_and_arguments(self):
        for target in TARGETS:
            with self.subTest(target=target):
                result, log = self.run_target(target)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assert_output_logged(result, log)
                args = self.args_file.read_text().splitlines()
                expected = ["test", "./...", "-v", "-failfast=false"]
                if target == "test-unit":
                    expected.append("-short")
                elif target == "test-coverage":
                    expected.append("-coverprofile=" + str(log.parent / "coverage.out"))
                self.assertEqual(args, expected)

    def test_go_failure_fails_make_and_keeps_logs(self):
        self.env["ROUTESERVER_TEST_GO_EXIT"] = "17"
        for target in TARGETS:
            with self.subTest(target=target):
                result, log = self.run_target(target)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Error 17", result.stderr)
                self.assert_output_logged(result, log)
                self.assertNotIn("Coverage report generated:", result.stdout)

    def test_log_failure_fails_make(self):
        self.failing_tee()
        for target in TARGETS:
            with self.subTest(target=target):
                result, _ = self.run_target(target)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Error 23", result.stderr)
                self.assertTrue(self.args_file.exists())
                self.assertNotIn("Coverage report generated:", result.stdout)

    def test_combined_failure_fails_make(self):
        self.env["ROUTESERVER_TEST_GO_EXIT"] = "17"
        self.failing_tee()
        for target in TARGETS:
            with self.subTest(target=target):
                result, _ = self.run_target(target)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("Coverage report generated:", result.stdout)

    def test_header_log_failure_does_not_start_go(self):
        self.executable("tee", "#!/bin/sh\ncat\nexit 23\n")
        for target in TARGETS:
            with self.subTest(target=target):
                result, _ = self.run_target(target)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.args_file.exists())

    def test_package_layout_matches_deploy_script(self):
        repo = MAKEFILE.parent
        for directory in ("cmd/route_server", "cmd/config-tool", "deploy/scale", "deploy/pipelines",
                          "deploy/k8s-configmap"):
            (self.root / directory).mkdir(parents=True)
        shutil.copyfile(repo / "deploy/deploy.sh", self.root / "deploy/deploy.sh")
        (self.root / "deploy/route-server-template.yaml").write_text("# fixture\n")
        (self.root / "deploy/k8s-configmap.yaml").write_text("# configmap fixture\n")
        (self.root / "deploy/k8s-configmap/rbac.yaml").write_text("# rbac fixture\n")
        fake_go = self.executable("go", '''#!/bin/sh
case "$1" in
    env) printf 'go1.26.0\\n'; exit 0 ;;
    version) printf 'go version go1.26.0 fixture/fixture\\n'; exit 0 ;;
    build)
        while [ "$#" -gt 0 ]; do
            if [ "$1" = "-o" ]; then
                shift
                mkdir -p "$(dirname "$1")"
                printf 'fixture binary\\n' > "$1"
                exit 0
            fi
            shift
        done ;;
esac
exit 99
''')
        # Any unexpected attempt to install a toolchain must fail without a network call.
        self.executable("curl", "#!/bin/sh\nexit 99\n")
        result = subprocess.run(
            ["make", "--no-print-directory", "-f", str(MAKEFILE), "package",
             "GO=" + str(fake_go), "VERSION=fixture", "BUILD_TIME=fixture"],
            cwd=self.root, env=self.env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        with zipfile.ZipFile(self.root / "output/route-server-fixture-fixture.zip") as archive:
            make_names = {name[2:] if name.startswith("./") else name for name in archive.namelist()}
        result = subprocess.run(
            ["bash", str(repo / ".codearts/build.sh")],
            cwd=self.root, env=self.env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        archives = list(self.root.glob("route-server-*-linux-*.tar.gz"))
        self.assertEqual(len(archives), 1)
        with tarfile.open(archives[0]) as archive:
            codearts_names = {name[2:] if name.startswith("./") else name for name in archive.getnames()}
        required = {"route-server/bin/route-server", "route-server/version.info",
                    "config_generator", "deploy.sh", "route-server-template.yaml",
                    "k8s-configmap.yaml", "k8s-configmap/rbac.yaml"}
        for names in (make_names, codearts_names):
            self.assertTrue(required.issubset(names), names)
            self.assertFalse(any("DistributedScheduler" in name for name in names))
        self.assertIn("PACKAGE_DIR=route-server\n", (self.root / "deploy/deploy.sh").read_text())

    def test_codearts_rejects_go_below_module_minimum(self):
        self.executable("go", "#!/bin/sh\nprintf 'go1.25.0\\n'\n")
        url_file = self.root / "toolchain-url"
        self.env["ROUTESERVER_TOOLCHAIN_URL"] = str(url_file)
        self.executable("curl", '''#!/bin/sh
printf '%s\\n' "$@" > "$ROUTESERVER_TOOLCHAIN_URL"
exit 43
''')
        result = subprocess.run(
            ["bash", str(MAKEFILE.parent / ".codearts/build.sh")],
            cwd=self.root, env=self.env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
        )
        self.assertEqual(result.returncode, 43, result.stdout + result.stderr)
        self.assertRegex(url_file.read_text(), r"go1\.26\.0\.linux-(amd64|arm64)\.tar\.gz")

    @unittest.skipUnless(shutil.which("go"), "Go toolchain is not available")
    def test_real_go_success_and_failure(self):
        (self.root / "go.mod").write_text("module example.invalid/makefile-fixture\n\ngo 1.26.0\n")
        (self.root / "fixture_test.go").write_text('''package fixture

import (
    "os"
    "testing"
)

func TestFixture(t *testing.T) {
    if os.Getenv("ROUTESERVER_TEST_FAIL") == "1" {
        t.Fatal("intentional fixture failure")
    }
    t.Log("fixture passed")
}
''')
        # Isolate caches/configuration and prohibit toolchain or dependency downloads.
        env = self.env.copy()
        env.update(GOCACHE=str(self.root / "go-cache"), GOTOOLCHAIN="local",
                   GOPROXY="off", GOSUMDB="off", GOWORK="off", GOENV="off", GOFLAGS="")
        real_go = shutil.which("go")
        for target in TARGETS:
            for fails in (False, True):
                with self.subTest(target=target, fails=fails):
                    env["ROUTESERVER_TEST_FAIL"] = "1" if fails else "0"
                    result, log = self.run_target(target, go=real_go, env=env)
                    self.assertEqual(result.returncode == 0, not fails, result.stdout + result.stderr)
                    marker = "intentional fixture failure" if fails else "fixture passed"
                    self.assertIn(marker, result.stdout)
                    self.assertIn(marker, log.read_text())
                    if target == "test-coverage":
                        if fails:
                            self.assertNotIn("Coverage report generated:", result.stdout)
                        else:
                            self.assertIn("Coverage report generated:", result.stdout)
                            self.assertTrue((log.parent / "coverage.out").is_file())


if __name__ == "__main__":
    unittest.main(verbosity=2)
