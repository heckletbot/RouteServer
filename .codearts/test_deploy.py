#!/usr/bin/env python3
# Copyright 2026 The RouteServer Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Run deploy.sh against isolated packages, without ports or external services."""
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class DeployTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="routeserver deploy test ")
        self.root = Path(self.tmp.name)
        self.addCleanup(self.cleanup)
        self.package = self.root / "package"
        shutil.copytree(ROOT / "deploy", self.package)
        self.commands = self.root / "commands"
        self.commands.mkdir()
        self.install = self.root / "install" / "fixture" / "route-server"
        (self.install / "config" / "pipelines").mkdir(parents=True)
        (self.install / "config" / "route-server.yaml").write_text("old config\n")
        (self.install / "config" / "pipelines" / "old.yaml").write_text("old pipeline\n")
        self.original = self.installed_files()
        (self.package / "route-server" / "bin").mkdir(parents=True)
        self.write_executable(self.package / "route-server" / "bin" / "route-server", f'''#!{sys.executable}
import os,time,pathlib
pathlib.Path(os.environ['FIXTURE_STARTED']).write_text(str(os.getpid()))
if os.environ.get('FIXTURE_START_EXIT'):
    raise SystemExit(int(os.environ['FIXTURE_START_EXIT']))
time.sleep(30)
''')
        self.write_executable(self.package / "config_generator", f'''#!{sys.executable}
import json,os,pathlib,sys
args=dict(zip(sys.argv[1::2],sys.argv[2::2]))
pathlib.Path(os.environ['FIXTURE_ARGS']).write_text(json.dumps(args))
if os.environ.get('FIXTURE_GENERATOR_EXIT'):
    raise SystemExit(int(os.environ['FIXTURE_GENERATOR_EXIT']))
if not os.environ.get('FIXTURE_NO_CONFIG'):
    (pathlib.Path(args['--target'])/'config'/'route-server.yaml').write_text('new config\\n')
''')
        self.write_executable(self.commands / "sleep", "#!/bin/sh\nexec /bin/sleep 0.3\n")
        self.write_executable(self.commands / "ps", '''#!/bin/sh
if [ "$1" = "-ax" ]; then
    if [ -n "$FIXTURE_PS_FAILURE" ]; then exit 27; fi
    cat "$FIXTURE_PROCESSES"
else
    kill -0 "$2" 2>/dev/null
fi
''')
        self.env = os.environ.copy()
        # Do not let ambient backend/cluster configuration change the fixture.
        for key in ["BASH_ENV", "ENV", "PREFILL_SERVERS", "DECODE_SERVERS", "ENCODE_SERVERS",
                    "MOE_SERVERS", "ETCD_ENDPOINTS", "GOMAXPROCS"]:
            self.env.pop(key, None)
        self.env.update(PATH=str(self.commands) + os.pathsep + self.env["PATH"],
                        RS_INSTALL_DIR=str(self.root / "install"), TMPDIR=str(self.root),
                        FIXTURE_ARGS=str(self.root / "args.json"),
                        FIXTURE_STARTED=str(self.root / "started"),
                        FIXTURE_PROCESSES=str(self.root / "processes"))
        (self.root / "processes").write_text("")

    def write_executable(self, path, contents):
        path.write_text(contents)
        path.chmod(0o755)

    def cleanup(self):
        marker = self.root / "started"
        if marker.exists():
            try:
                os.kill(int(marker.read_text()), signal.SIGTERM)
            except ProcessLookupError:
                pass
        for directory, _, _ in os.walk(self.root):
            os.chmod(directory, 0o700)
        self.tmp.cleanup()

    def installed_files(self):
        return {str(p.relative_to(self.install)): p.read_bytes()
                for p in self.install.rglob("*") if p.is_file()}

    def run_deploy(self, *args):
        return subprocess.run(["bash", str(self.package / "deploy.sh"), "--rs_name", "fixture",
                               "--host", "127.0.0.1", *args], env=self.env, cwd=self.root,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=15)

    def assert_preflight_failure(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(self.installed_files(), self.original)
        self.assertFalse((self.root / "started").exists(), result.stdout)
        self.assertFalse(list(self.root.glob("route-server-deploy.*")))

    def test_help_without_cpu_detection(self):
        self.write_executable(self.commands / "getconf", "#!/bin/sh\nexit 29\n")
        result = self.run_deploy("--help")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("Usage:", result.stdout)

    def test_missing_or_unreadable_server_tls_is_rejected_before_install(self):
        for args in [("--enable-https", "true"), ("--ops-enable-https", "true"),
                     ("--enable-https", "true", "--enable-mtls", "false",
                      "--tls-cert-file", "/missing/cert", "--tls-key-file", "/missing/key")]:
            with self.subTest(args=args):
                self.assert_preflight_failure(self.run_deploy(*args))
                self.assertFalse((self.root / "args.json").exists())

    def test_enabled_client_tls_rejects_missing_supplied_files(self):
        for plane, cert in [("--client-https-enable", "--client-ca-cert-file"),
                            ("--etcd-tls-enable", "--etcd-cert-file")]:
            with self.subTest(plane=plane):
                self.assert_preflight_failure(self.run_deploy(plane, "true", cert, "/missing/file"))

    def test_generator_failure_preserves_install_and_exit_code(self):
        self.env["FIXTURE_GENERATOR_EXIT"] = "19"
        result = self.run_deploy()
        self.assert_preflight_failure(result)
        self.assertEqual(result.returncode, 19)

    def test_generator_success_without_new_config_is_rejected(self):
        config = self.package / "route-server" / "config"
        config.mkdir()
        (config / "route-server.yaml").write_text("stale packaged config\n")
        self.env["FIXTURE_NO_CONFIG"] = "1"
        result = self.run_deploy()
        self.assert_preflight_failure(result)
        self.assertIn("did not produce", result.stdout)

    def test_copy_and_rewrite_failures_do_not_reach_generator_or_install(self):
        for command in ["cp", "sed"]:
            with self.subTest(command=command):
                fake = self.commands / command
                self.write_executable(fake, "#!/bin/sh\nexit 23\n")
                self.assert_preflight_failure(self.run_deploy())
                self.assertFalse((self.root / "args.json").exists())
                fake.unlink()

    def test_cpu_detection_failure_and_invalid_value_are_rejected(self):
        self.assert_preflight_failure(self.run_deploy("--max-procs", "invalid"))
        self.write_executable(self.commands / "getconf", "#!/bin/sh\nexit 29\n")
        result = self.run_deploy("--max-procs", "1")
        self.assert_preflight_failure(result)
        self.assertEqual(result.returncode, 1)

    def test_ps_failure_does_not_replace_installation(self):
        self.env["FIXTURE_PS_FAILURE"] = "1"
        self.assert_preflight_failure(self.run_deploy())

    def test_stop_failure_or_timeout_preserves_installation(self):
        binary = str(self.install / "bin" / "route-server")
        (self.root / "processes").write_text(f" 100 {binary}\n")
        hooks = self.root / "hooks"
        self.env["BASH_ENV"] = str(hooks)
        for fail_signal in (True, False):
            with self.subTest(fail_signal=fail_signal):
                hooks.write_text('kill() {\n'
                                 '  if [[ "$1" == "-0" ]]; then return 0; fi\n'
                                 f'  return {1 if fail_signal else 0}\n' + '}\n')
                result = self.run_deploy()
                self.assert_preflight_failure(result)
                self.assertIn("Failed", result.stdout)

    def test_success_keeps_http_defaults_backup_and_final_log_path(self):
        result = self.run_deploy("--max-procs", "1")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue((self.root / "started").exists())
        args = json.loads((self.root / "args.json").read_text())
        for flag in ["--enable-https", "--ops-enable-https", "--client-https-enable", "--etcd-tls-enable"]:
            self.assertEqual(args[flag], "false")
        self.assertEqual(args["--log-file-path"], str(self.install / "log" / "route-server.log"))
        self.assertEqual((self.install / "config" / "route-server.yaml.bak").read_text(), "old config\n")
        self.assertFalse((self.install / "config" / "pipelines" / "old.yaml").exists())
        self.assertFalse(list(self.root.glob("route-server-deploy.*")))
        for metrics in (self.install / "config").glob("metrics_config*.yaml"):
            self.assertIn("collect_from_uniq_ip: false", metrics.read_text())

    def test_pipeline_switches_and_metrics_rewrite(self):
        result = self.run_deploy("--infer-mode", "VA-PD", "--reuse-prefilled-tokens",
                                 "--collect-metrics-unique-ip", "true")
        self.assertEqual(result.returncode, 0, result.stdout)
        pipelines = self.install / "config" / "pipelines"
        pd = (pipelines / "pd_separate.yaml").read_text()
        self.assertIn("          - vllm_ascend_prepare_request\n", pd)
        self.assertIn("type: vllm_ascend_fail", pd)
        self.assertIn("prefill_stream_prepare_request", (pipelines / "metaserver_cdcp.yaml").read_text())
        self.assertIn("prefill_stream_prepare_request", (pipelines / "layerwise_cpcd.yaml").read_text())
        for metrics in (self.install / "config").glob("metrics_config*.yaml"):
            self.assertIn("collect_from_uniq_ip: true", metrics.read_text())
        self.assertEqual(json.loads((self.root / "args.json").read_text())["--infer-mode"], "PD")

    def test_startup_failure_reports_child_status(self):
        self.env["FIXTURE_START_EXIT"] = "17"
        result = self.run_deploy()
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertIn("retCode: 17", result.stdout)

    def test_daemon_propagates_child_status(self):
        # Let the first liveness check pass, then wait observes the failed child.
        self.env["FIXTURE_START_EXIT"] = "18"
        self.write_executable(self.commands / "ps", "#!/bin/sh\nexit 0\n")
        result = self.run_deploy("--daemon", "true")
        self.assertEqual(result.returncode, 18, result.stdout)

    def test_only_exact_installation_binary_is_stopped(self):
        binary = str(self.install / "bin" / "route-server")
        (self.root / "processes").write_text(
            f" 100 {binary}\n 101 {binary}-other\n 102 echo {binary}\n 103 {binary}/child\n")
        # Substitute only signals against controlled fixture PIDs. Real process
        # liveness checks use ps and the child still starts normally.
        hooks = self.root / "hooks"
        hooks.write_text('''kill() {
  if [[ "$1" == "-0" ]]; then
    [[ ! -f "$FIXTURE_KILLED" ]]
  else
    echo "$*" >> "$FIXTURE_KILLED"
  fi
}
''')
        self.env.update(BASH_ENV=str(hooks), FIXTURE_KILLED=str(self.root / "killed"))
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.root / "killed").read_text().splitlines(), ["100"])


if __name__ == "__main__":
    unittest.main()
