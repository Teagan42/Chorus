"""MkDocs hooks that publish the repo's Markdown as the site, never a copy of it.

The site reads docs/ in place and adds the root Markdown (README, CONTRIBUTING,
...) as pages, so there is no second copy to drift. Links are written for
GitHub, relative to each file in the repo; here they are re-pointed to the
page that file became, or to the source at the commit the site was built
from. A link that resolves to neither is left alone, so `mkdocs build
--strict` names it and the build fails.
"""

from __future__ import annotations

import glob
import json
import logging
import os
import posixpath
import re

from mkdocs.config.defaults import MkDocsConfig
from mkdocs.exceptions import PluginError
from mkdocs.structure.files import File, Files
from mkdocs.structure.pages import Page

# Inline links and images: [text](target "title"). Reference-style links are
# not used in this repo; add them here before using one.
LINK = re.compile(r"(!?\[(?:[^\[\]]|\[[^\]]*\])*\]\()(<[^>]*>|[^)\s]+)((?:\s+\"[^\"]*\")?\))")
FENCE = re.compile(r"^(\s*)(`{3,}|~{3,})")
# Words after a fence's language that are docexec's, not the highlighter's.
DOCEXEC_TAGS = re.compile(r"[ \t]+norun\b")
INLINE_CODE = re.compile(r"(`+)(?:(?!\1).)+?\1")
SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")

# Under "mkdocs", so a warning here fails a strict build like MkDocs' own.
log = logging.getLogger("mkdocs.hooks.docsite")


def repo_root(config: MkDocsConfig) -> str:
    return os.path.dirname(os.path.abspath(config.config_file_path))


def docs_prefix(config: MkDocsConfig) -> str:
    """The docs dir as a repo-relative path, e.g. "docs"."""
    return posixpath.relpath(
        os.path.abspath(config.docs_dir).replace(os.sep, "/"),
        repo_root(config).replace(os.sep, "/"),
    )


def root_pages(config: MkDocsConfig) -> dict[str, str]:
    """Repo path → site path for Markdown outside the docs dir."""
    return dict(config.extra.get("docsite", {}).get("pages", {}))


def source_ref() -> str:
    """The commit the site is built from, so a source link shows that code."""
    return os.environ.get("GITHUB_SHA") or "main"


def released_version(manifest_path: str) -> str:
    """The tag of the last release, from release-please's manifest."""
    try:
        with open(manifest_path) as f:
            version = json.load(f)["."]
    except (OSError, ValueError, KeyError) as e:
        raise PluginError(f"read the released version from {manifest_path}: {e!r}") from e
    return f"v{version}"


def expand_nav(nav, docs_dir: str, excluded=lambda path: False):
    """Expand nav entries holding a glob into the files they match, in order.

    A new ADR then joins the nav by existing; a hand-kept list would go stale.
    """
    if isinstance(nav, list):
        out = []
        for item in nav:
            if isinstance(item, str) and "*" in item:
                matches = sorted(
                    posixpath.relpath(p.replace(os.sep, "/"), docs_dir.replace(os.sep, "/"))
                    for p in glob.glob(os.path.join(docs_dir, item))
                )
                out.extend(m for m in matches if not excluded(m))
            else:
                out.append(expand_nav(item, docs_dir, excluded))
        return out
    if isinstance(nav, dict):
        return {k: expand_nav(v, docs_dir, excluded) for k, v in nav.items()}
    return nav


def split_target(target: str) -> tuple[str, str]:
    path, sep, frag = target.partition("#")
    return path, sep + frag


def rewrite_links(
    markdown: str,
    page_repo_path: str,
    page_site_path: str,
    site_paths: dict[str, str],
    exists,
    is_dir,
    repo_url: str,
    ref: str,
) -> str:
    """Re-point each relative link in markdown from repo paths to site paths.

    site_paths maps every published repo path to its site path. exists and
    is_dir answer for repo paths, so a link to source code lands on GitHub
    at ref.
    """
    page_dir = posixpath.dirname(page_repo_path)
    site_dir = posixpath.dirname(page_site_path)

    def resolve(target: str, image: bool) -> str | None:
        bare = target[1:-1] if target.startswith("<") else target
        if not bare or bare.startswith(("#", "/")) or SCHEME.match(bare):
            return None
        path, frag = split_target(bare)
        repo_path = posixpath.normpath(posixpath.join(page_dir, path))
        if repo_path.startswith("../"):
            return None
        if repo_path not in site_paths and is_dir(repo_path):
            index = posixpath.join(repo_path, "README.md")
            if index in site_paths:
                repo_path = index
            else:
                return f"{repo_url}/tree/{ref}/{repo_path}{frag}"
        if repo_path in site_paths:
            return posixpath.relpath(site_paths[repo_path], site_dir or ".") + frag
        if exists(repo_path):
            kind = "raw" if image else "blob"
            return f"{repo_url}/{kind}/{ref}/{repo_path}{frag}"
        # MkDocs names a broken link itself, but not a broken image.
        if image:
            log.warning("%s shows image '%s', which does not exist", page_repo_path, bare)
        return None

    def fix_line(line: str) -> str:
        # A link inside a code span is prose about a link; code inside a
        # link's text is still a link.
        spans = [m.span() for m in INLINE_CODE.finditer(line)]

        def sub(m: re.Match) -> str:
            if any(a <= m.start() < b for a, b in spans):
                return m.group(0)
            new = resolve(m.group(2), m.group(1).startswith("!"))
            return m.group(0) if new is None else f"{m.group(1)}{new}{m.group(3)}"

        return LINK.sub(sub, line)

    out = []
    fence = None
    for line in markdown.split("\n"):
        f = FENCE.match(line)
        if fence is None:
            if f:
                fence = f.group(2)
                # "```sh norun" is a fence to docexec; superfences reads the
                # bare word as a bad option and never opens the block.
                out.append(line[: f.end()] + DOCEXEC_TAGS.sub("", line[f.end() :]))
            else:
                out.append(fix_line(line))
        else:
            if f and f.group(2).startswith(fence[0] * len(fence)) and not line[f.end() :].strip():
                fence = None
            out.append(line)
    return "\n".join(out)


# ---------------------------------------------------------------- MkDocs hooks


def on_config(config: MkDocsConfig) -> MkDocsConfig:
    excluded = config.exclude_docs.match_file if config.exclude_docs else (lambda p: False)
    config.nav = expand_nav(config.nav, config.docs_dir, excluded)
    docsite = config.extra.setdefault("docsite", {})
    if docsite.get("version_from"):
        manifest = os.path.join(repo_root(config), docsite["version_from"])
        docsite["version"] = released_version(manifest)
    return config


def on_files(files: Files, config: MkDocsConfig) -> Files:
    root = repo_root(config)
    for repo_path, site_path in root_pages(config).items():
        files.append(File.generated(config, site_path, abs_src_path=os.path.join(root, repo_path)))
    return files


def on_page_markdown(markdown: str, page: Page, config: MkDocsConfig, files: Files) -> str:
    root = repo_root(config)
    prefix = docs_prefix(config)
    pages = root_pages(config)
    by_site = {v: k for k, v in pages.items()}

    docs_dir = os.path.abspath(config.docs_dir) + os.sep
    # Theme assets are files too; only what the repo holds has a repo path.
    site_paths = {
        by_site.get(f.src_uri, posixpath.join(prefix, f.src_uri)): f.src_uri
        for f in files
        if f.inclusion.is_included()
        and (f.src_uri in by_site or (f.abs_src_path or "").startswith(docs_dir))
    }
    page_site_path = page.file.src_uri
    page_repo_path = by_site.get(page_site_path, posixpath.join(prefix, page_site_path))

    repo_url = config.repo_url.rstrip("/")
    if page_site_path in by_site:
        page.edit_url = f"{repo_url}/edit/main/{page_repo_path}"

    return rewrite_links(
        markdown,
        page_repo_path,
        page_site_path,
        site_paths,
        exists=lambda p: os.path.exists(os.path.join(root, p)),
        is_dir=lambda p: os.path.isdir(os.path.join(root, p)),
        repo_url=repo_url,
        ref=source_ref(),
    )
