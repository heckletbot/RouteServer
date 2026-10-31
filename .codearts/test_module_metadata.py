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

"""Build real artifacts in a temporary snapshot and check the existing GET /."""

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]


def run(command, cwd, env):
    result = subprocess.run(command, cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
    if result.returncode:
        raise RuntimeError("command failed: " + repr(command) + "\n" + result.stdout[-12000:])
    return result.stdout


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def probe(snapshot, target, expected, env):
    output = snapshot / "output"
    binary = output / "route-server/bin/route-server"
    build_info = run(["go", "version", "-m", str(binary)], snapshot, env)
    if "\tmod\troute-server\t" not in build_info:
        raise AssertionError(build_info)
    config = target / "config"
    shutil.copytree(snapshot / "deploy/pipelines", config / "pipelines")
    for path in (snapshot / "deploy").glob("*.yaml"):
        shutil.copyfile(path, config / path.name)
    port, ops_port = free_port(), free_port()
    while ops_port == port:
        ops_port = free_port()
    run([str(output / "config_generator"), "-template", str(snapshot / "deploy/route-server-template.yaml"),
         "-target", str(target), "-host", "127.0.0.1", "-port", str(port), "-ops-port", str(ops_port),
         "-name", "module-metadata-check", "-mode", "singleton", "-infer-mode", "PD",
         "-infer-module-version", "pipeline", "-remote-scheduler-type", "disable",
         "-prefills", "prefill#127.0.0.1:1", "-decodes", "decode#127.0.0.1:2",
         "-enable-https", "false", "-ops-enable-https", "false",
         "-client-https-enable", "false", "-etcd-tls-enable", "false"], snapshot, env)
    server_env = env.copy()
    server_env["ENV_ROUTE_SERVER_CONFIG_PATH"] = str(config)
    server_env["ENV_ROUTE_SERVER_LOG_PATH"] = str(target / "log")
    (target / "log").mkdir(exist_ok=True)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    actual = None
    with (target / "server.log").open("w") as log:
        process = subprocess.Popen([str(binary)], cwd=target, env=server_env,
                                   stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError((target / "server.log").read_text()[-12000:])
                try:
                    with opener.open("http://127.0.0.1:%d/" % port, timeout=1) as response:
                        actual = json.load(response)
                    break
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(0.1)
            wanted = dict(expected, name="RouteServer", node_name="module-metadata-check", port_type="service_port")
            if actual != wanted:
                raise AssertionError("metadata %r != %r" % (actual, wanted))
            return actual
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--producer", choices=("make", "codearts", "both"), default="both")
    args = parser.parse_args()
    env = os.environ.copy()
    host = json.loads(run(["go", "env", "-json", "GOHOSTOS", "GOHOSTARCH"], ROOT, env))
    env.update(GOOS=host["GOHOSTOS"], GOARCH=host["GOHOSTARCH"], CGO_ENABLED="0")
    with tempfile.TemporaryDirectory(prefix="routeserver-module-metadata-") as directory:
        snapshot = Path(directory) / "source"
        snapshot.mkdir()
        files = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT).decode().split("\0")
        for name in filter(None, files):
            target = snapshot / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        producers = ("make", "codearts") if args.producer == "both" else (args.producer,)
        for producer in producers:
            shutil.rmtree(snapshot / "output", ignore_errors=True)
            if producer == "make":
                expected = dict(version="module-version", tag_or_branch="module-branch",
                                commit_id="module-commit", build_time="module-time")
                run(["make", "package", "VERSION=" + expected["version"], "BRANCH=" + expected["tag_or_branch"],
                     "COMMIT=" + expected["commit_id"], "BUILD_TIME=" + expected["build_time"]], snapshot, env)
            else:
                run(["bash", ".codearts/build.sh"], snapshot, env)
                values = (snapshot / "output/route-server/version.info").read_text().splitlines()
                branch, commit, built = (line.split(": ", 1)[1] for line in values)
                expected = dict(version="dev", tag_or_branch=branch, commit_id=commit, build_time=built)
            target = Path(directory) / producer
            actual = probe(snapshot, target, expected, env)
            print(json.dumps(dict(producer=producer, host=host, metadata=actual), sort_keys=True))
    if host["GOHOSTOS"] != "linux":
        print("Native-host linker/runtime smoke only; Linux release runner validation is still required.")


if __name__ == "__main__":
    main()
