from __future__ import annotations

import json
import pathlib
import tempfile
import unittest
from typing import final

from scripts.mac_test_runner import fixtures, state


@final
class CreatedFixturesTest(unittest.TestCase):
    def __init__(self, methodName: str = "runTest") -> None:
        super().__init__(methodName)
        self.temporary: tempfile.TemporaryDirectory[str] = tempfile.TemporaryDirectory()
        self.original_output: pathlib.Path | None = None

    def setUp(self) -> None:
        self.original_output = state.OUTPUT if hasattr(state, "OUTPUT") else None
        state.OUTPUT = pathlib.Path(self.temporary.name)

    def tearDown(self) -> None:
        if self.original_output is None:
            del state.OUTPUT
        else:
            state.OUTPUT = self.original_output
        self.temporary.cleanup()

    def add_event(self, identity: str, name: str, managed: str = "true") -> None:
        event = {
            "Type": "container",
            "Action": "create",
            "Actor": {
                "ID": identity,
                "Attributes": {"name": name, state.MANAGED: managed},
            },
        }
        with (state.OUTPUT / "events.jsonl").open("a") as output:
            output.write(json.dumps(event) + "\n")

    def test_logged_cluster_members_are_attributed(self) -> None:
        prefix = "tack-testenv-search-103-6002bcf3"
        (state.OUTPUT / "runtime.log").write_text(
            "testenv.cluster.started cluster=" + prefix + " planned=3\n"
        )
        self.add_event("node-1", prefix + "-1")
        self.add_event("node-2", prefix + "-2")
        self.add_event("proxy", prefix + "-proxy")

        self.assertEqual(fixtures.created_fixtures(), ["node-1", "node-2", "proxy"])
        self.assertEqual(
            (state.OUTPUT / "inner-process-ids.json").read_text(), '["103"]\n'
        )
        self.assertEqual((state.OUTPUT / "fixture-attribution.exit").read_text(), "0\n")

    def test_unknown_managed_events_remain_unattributed(self) -> None:
        prefix = "tack-testenv-search-103-6002bcf3"
        (state.OUTPUT / "runtime.log").write_text(
            "testenv.cluster.started cluster=" + prefix + " planned=3\n"
        )
        self.add_event("known", prefix + "-3")
        self.add_event("zero", prefix + "-0")
        self.add_event("unknown-suffix", prefix + "-peer")
        self.add_event("unlogged", "tack-testenv-search-103-deadbeef-1")
        self.add_event("unmanaged", prefix + "-4", managed="false")
        self.add_event("unrelated", "mwan-npt-networkd-proof", managed="false")

        self.assertEqual(fixtures.created_fixtures(), ["known"])
        self.assertEqual(
            json.loads((state.OUTPUT / "unattributed-preserved-ids.json").read_text()),
            ["zero", "unknown-suffix", "unlogged"],
        )
        self.assertEqual((state.OUTPUT / "fixture-attribution.exit").read_text(), "1\n")

    def test_multiple_process_ids_fail(self) -> None:
        first = "tack-testenv-search-103-6002bcf3"
        second = "tack-testenv-search-104-deadbeef"
        (state.OUTPUT / "runtime.log").write_text(first + "\n" + second + "\n")
        self.add_event("first", first + "-1")
        self.add_event("second", second + "-1")

        with self.assertRaisesRegex(RuntimeError, "multiple test process identities"):
            fixtures.created_fixtures()


if __name__ == "__main__":
    unittest.main()
