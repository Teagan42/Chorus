"""The docs site hooks: link rewriting in isolation, then real MkDocs builds.

The unit tests feed rewrite_links the shapes the repo's Markdown actually
holds. The build tests run MkDocs itself, once over this repo and once over
a small household-shaped repo in a temp dir, where a doc can be broken on
purpose to prove the strict build refuses it.
"""

from __future__ import annotations

import os
import re
import textwrap
from pathlib import Path

import hooks
import pytest
from mkdocs.commands.build import build
from mkdocs.config import load_config
from mkdocs.exceptions import Abort

REPO = Path(__file__).resolve().parents[3]
URL = "https://github.com/Teagan42/Chorus"

# The repo as rewrite_links sees it: what is published, and what else exists.
SITE = {
    "README.md": "index.md",
    "CONTRIBUTING.md": "contributing.md",
    "docs/SPEC.md": "SPEC.md",
    "docs/adr/README.md": "adr/README.md",
    "docs/adr/0004-barge-in-detection-gate.md": "adr/0004-barge-in-detection-gate.md",
    "docs/reviewui/README.md": "reviewui/README.md",
    "docs/reviewui/triage-slow.png": "reviewui/triage-slow.png",
    "docs/reviewui/e2e/journey-browse-today.png": "reviewui/e2e/journey-browse-today.png",
}
SOURCE_DIRS = {"internal", "internal/session", "docs", "docs/adr", "docs/reference"}
SOURCE_FILES = {"Taskfile.yml", "docs/adr/0000-template.md", "cmd/reviewui/e2e_test.go"}


def rewrite(markdown: str, repo_path: str, site_path: str, ref: str = "main") -> str:
    return hooks.rewrite_links(
        markdown,
        repo_path,
        site_path,
        SITE,
        exists=lambda p: p in SOURCE_FILES or p in SOURCE_DIRS or p in SITE,
        is_dir=lambda p: p in SOURCE_DIRS,
        repo_url=URL,
        ref=ref,
    )


# ------------------------------------------------------------- rewrite_links


def test_readme_links_into_docs_become_site_pages():
    md = "| [`docs/SPEC.md`](docs/SPEC.md) | Normative. Tests cite the clause they verify. |"
    assert rewrite(md, "README.md", "index.md") == (
        "| [`docs/SPEC.md`](SPEC.md) | Normative. Tests cite the clause they verify. |"
    )


def test_a_directory_link_lands_on_its_readme():
    md = "Every decision is in [`docs/adr/`](docs/adr/)."
    assert (
        rewrite(md, "README.md", "index.md") == "Every decision is in [`docs/adr/`](adr/README.md)."
    )


def test_a_directory_without_a_readme_lands_on_github():
    md = "Generated from CUE: [`docs/reference/`](docs/reference/)."
    assert rewrite(md, "README.md", "index.md") == (
        f"Generated from CUE: [`docs/reference/`]({URL}/tree/main/docs/reference)."
    )


def test_source_code_links_land_on_github_at_the_built_commit():
    md = (
        "The supervisor lives in [session](../internal/session/) "
        "and runs off [the Taskfile](../Taskfile.yml#L12)."
    )
    out = rewrite(md, "docs/SPEC.md", "SPEC.md", ref="b836e39")
    assert out == (
        f"The supervisor lives in [session]({URL}/tree/b836e39/internal/session) "
        f"and runs off [the Taskfile]({URL}/blob/b836e39/Taskfile.yml#L12)."
    )


def test_an_excluded_page_lands_on_its_source():
    md = "Start from [`0000-template.md`](0000-template.md)."
    assert rewrite(md, "docs/adr/README.md", "adr/README.md") == (
        f"Start from [`0000-template.md`]({URL}/blob/main/docs/adr/0000-template.md)."
    )


def test_anchors_survive_the_rewrite():
    md = "See [the barge-in gate](docs/adr/0004-barge-in-detection-gate.md#alternatives-rejected)."
    assert rewrite(md, "README.md", "index.md") == (
        "See [the barge-in gate](adr/0004-barge-in-detection-gate.md#alternatives-rejected)."
    )


def test_a_page_in_docs_linking_back_to_the_root_reaches_the_virtual_page():
    md = "The test tiers are in [CONTRIBUTING](../../CONTRIBUTING.md#1-tdd)."
    assert rewrite(md, "docs/reviewui/README.md", "reviewui/README.md") == (
        "The test tiers are in [CONTRIBUTING](../contributing.md#1-tdd)."
    )


def test_screenshots_keep_working_from_the_page_that_shows_them():
    md = "![Triage, slow](triage-slow.png)\n![Journey: Browse today](e2e/journey-browse-today.png)"
    assert rewrite(md, "docs/reviewui/README.md", "reviewui/README.md") == md


def test_an_image_outside_docs_is_served_raw():
    SOURCE_FILES.add("esphome/satellite1.png")
    try:
        out = rewrite("![wiring](esphome/satellite1.png)", "README.md", "index.md")
    finally:
        SOURCE_FILES.discard("esphome/satellite1.png")
    assert out == f"![wiring]({URL}/raw/main/esphome/satellite1.png)"


def test_a_missing_screenshot_is_a_warning(caplog):
    md = "![Curate, the mismatch guard](curate-guard.png)"
    assert rewrite(md, "docs/reviewui/README.md", "reviewui/README.md") == md
    assert "shows image 'curate-guard.png', which does not exist" in caplog.text


def test_a_link_to_nothing_is_left_for_mkdocs_to_name():
    md = "[the old harvester](../internal/dpo/)"
    assert rewrite(md, "docs/SPEC.md", "SPEC.md") == md


def test_urls_anchors_and_mail_are_untouched():
    md = (
        "[uv](https://docs.astral.sh/uv/) · [§4.3](#43-barge-in) · "
        "[mail](mailto:teagan.glenn@example.com) · [abs](/Chorus/)"
    )
    assert rewrite(md, "README.md", "index.md") == md


def test_code_is_prose_about_links_not_links():
    md = textwrap.dedent(
        """\
        Write `[spec](docs/SPEC.md)` to link the spec.

        ```markdown
        [spec](docs/SPEC.md)
        ```

        ````
        ```go norun
        // [adr](docs/adr/)
        ```
        ````

        But [the spec](docs/SPEC.md) is a link.
        """
    )
    out = rewrite(md, "README.md", "index.md")
    assert out.count("(docs/SPEC.md)") == 2
    assert "(docs/adr/)" in out
    assert out.endswith("But [the spec](SPEC.md) is a link.\n")


def test_link_text_with_brackets_still_rewrites():
    md = "[SPEC §8 [journal]](docs/SPEC.md#8-event-journal)"
    assert rewrite(md, "README.md", "index.md") == "[SPEC §8 [journal]](SPEC.md#8-event-journal)"


# ---------------------------------------------------------------- expand_nav


def test_a_new_adr_joins_the_nav_by_existing(tmp_path):
    adr = tmp_path / "adr"
    adr.mkdir()
    for name in ["README.md", "0000-template.md", "0035-the-first-played-frame-is-an-event.md"]:
        (adr / name).write_text("# x\n")
    nav = [{"Decisions": ["adr/README.md", "adr/[0-9]*.md"]}]

    first = hooks.expand_nav(nav, str(tmp_path), lambda p: p == "adr/0000-template.md")
    (adr / "0036-speaker-flips-re-attribute-the-turn.md").write_text("# x\n")
    second = hooks.expand_nav(nav, str(tmp_path), lambda p: p == "adr/0000-template.md")

    assert first == [
        {"Decisions": ["adr/README.md", "adr/0035-the-first-played-frame-is-an-event.md"]}
    ]
    assert second[0]["Decisions"][-1] == "adr/0036-speaker-flips-re-attribute-the-turn.md"


def test_nav_entries_without_a_glob_pass_through(tmp_path):
    nav = ["index.md", {"Spec": "SPEC.md"}, {"Reference": [{"Bridge protocol": "esphome.md"}]}]
    assert hooks.expand_nav(nav, str(tmp_path)) == nav


# ------------------------------------------------------- building this repo


@pytest.fixture(scope="module")
def built(tmp_path_factory):
    site = tmp_path_factory.mktemp("site")
    cfg = load_config(str(REPO / "mkdocs.yml"), site_dir=str(site))
    build(cfg)
    return site


def test_the_readme_is_the_home_page(built):
    html = (built / "index.html").read_text()
    assert "Your voice assistant shouldn&rsquo;t go deaf while it talks." in html or (
        "Your voice assistant shouldn't go deaf while it talks." in html
    )
    assert 'href="SPEC/"' in html
    assert 'href="reviewui/"' in html
    assert f'href="{URL}/edit/main/README.md"' in html


def test_every_adr_is_published_and_the_template_is_not(built):
    adrs = sorted(p.name for p in (REPO / "docs/adr").glob("[0-9]*.md"))
    for name in adrs:
        published = built / "adr" / name.removesuffix(".md") / "index.html"
        assert published.exists() == (name != "0000-template.md"), name


def test_contributing_edits_the_real_file(built):
    html = (built / "contributing" / "index.html").read_text()
    assert f'href="{URL}/edit/main/CONTRIBUTING.md"' in html
    assert "Five rules." in html


def test_the_review_ui_guide_shows_its_screenshots(built):
    html = (built / "reviewui" / "index.html").read_text()
    referenced = re.findall(r"\]\(([\w/-]+\.png)\)", (REPO / "docs/reviewui/README.md").read_text())
    assert referenced, "the guide shows no screenshots"
    for png in referenced:
        assert (built / "reviewui" / png).exists(), png
        assert png in html, png


def test_the_architecture_diagram_is_left_for_mermaid(built):
    html = (built / "index.html").read_text()
    assert '<pre class="mermaid"><code>flowchart LR' in html


# ------------------------------------------------- building a broken repo


def household_repo(root: Path) -> Path:
    """A repo in miniature: a README, a spec, an ADR, and the code they cite."""
    (root / "docs/adr").mkdir(parents=True)
    (root / "internal/session").mkdir(parents=True)
    (root / "internal/session/supervisor.go").write_text("package session\n")
    (root / "README.md").write_text(
        "# Chorus\n\nRead [the spec](docs/SPEC.md) and [the decisions](docs/adr/).\n"
    )
    (root / "docs/SPEC.md").write_text(
        "# Spec\n\n## 4. Sessions\n\n"
        "One supervisor per satellite: [supervisor.go](../internal/session/supervisor.go).\n"
    )
    (root / "docs/adr/README.md").write_text(
        "# Decisions\n\n- [0004](0004-barge-in-detection-gate.md)\n"
    )
    (root / "docs/adr/0004-barge-in-detection-gate.md").write_text(
        "# 4. Gate barge-in on detection\n\nSee [SPEC §4](../SPEC.md#4-sessions).\n"
    )
    (root / "mkdocs.yml").write_text(
        textwrap.dedent(
            f"""\
            site_name: Chorus
            repo_url: {URL}
            strict: true
            validation:
              omitted_files: warn
              absolute_links: warn
              unrecognized_links: warn
              anchors: warn
            hooks: [{Path(hooks.__file__).as_posix()}]
            extra:
              docsite:
                pages:
                  README.md: index.md
            nav:
              - index.md
              - SPEC.md
              - Decisions: [adr/README.md, "adr/[0-9]*.md"]
            """
        )
    )
    return root


def build_repo(root: Path) -> Path:
    site = root / "site"
    build(load_config(str(root / "mkdocs.yml"), site_dir=str(site)))
    return site


def test_the_household_repo_builds(tmp_path):
    site = build_repo(household_repo(tmp_path))
    spec = (site / "SPEC" / "index.html").read_text()
    assert f'href="{URL}/blob/main/internal/session/supervisor.go"' in spec


def test_a_doc_citing_deleted_code_fails_the_build(tmp_path):
    root = household_repo(tmp_path)
    os.remove(root / "internal/session/supervisor.go")
    with pytest.raises(Abort):
        build_repo(root)


def test_a_renamed_spec_heading_fails_the_build(tmp_path):
    root = household_repo(tmp_path)
    spec = root / "docs/SPEC.md"
    spec.write_text(spec.read_text().replace("## 4. Sessions", "## 4. The session supervisor"))
    with pytest.raises(Abort):
        build_repo(root)


def test_a_doc_missing_from_the_nav_fails_the_build(tmp_path):
    root = household_repo(tmp_path)
    (root / "docs/reviewui.md").write_text("# Review UI\n\nBrowse, Triage, Review.\n")
    with pytest.raises(Abort):
        build_repo(root)


def test_a_new_adr_needs_no_nav_edit(tmp_path):
    root = household_repo(tmp_path)
    (root / "docs/adr/0005-interrupted-turn-truth.md").write_text(
        "# 5. The interrupted turn is what was played\n"
    )
    site = build_repo(root)
    assert (site / "adr" / "0005-interrupted-turn-truth" / "index.html").exists()


def test_docexec_tags_leave_the_fence_and_examples_of_them_stay():
    md = textwrap.dedent(
        """\
        ```sh norun
        task reviewui -- -addr :9090   # somewhere other than :8080
        ```

        Opt a block out with `norun`:

        ````
        ```go norun
        // a sketch, not a compilable example
        ```
        ````
        """
    )
    out = rewrite(md, "CONTRIBUTING.md", "contributing.md")
    assert out.startswith("```sh\ntask reviewui")
    assert "````\n```go norun\n// a sketch" in out


def test_screens_after_a_norun_block_are_still_images(built):
    html = (built / "reviewui" / "index.html").read_text()
    assert '<img alt="Browse" src="browse.png"' in html
