from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github/workflows/deploy-test-cn.yml"


def text() -> str:
    return WORKFLOW.read_text(encoding="utf-8")


def test_cn_test_workflow_is_main_push_or_main_dispatch_and_arm64() -> None:
    workflow = text()
    assert "name: Deploy China Test" in workflow
    assert "\n  push:\n    branches: [main]\n" in workflow
    assert "\n  workflow_dispatch:\n" in workflow
    assert "if: github.ref == 'refs/heads/main'" in workflow
    assert "runs-on: [self-hosted, Linux, ARM64, botlearn-runner-us]" in workflow
    assert "fetch-depth: 0" in workflow
    assert 'test "$(uname -m)" = aarch64' in workflow
    assert "docker buildx build --platform linux/arm64 --push" in workflow


def test_cn_test_workflow_only_pushes_an_immutable_candidate() -> None:
    workflow = text()
    assert "ECR_REPOSITORY: open/open-newapi" in workflow
    assert "CANDIDATE_TAG: cn-test-${{ github.sha }}" in workflow
    assert "Resolve immutable candidate" in workflow
    assert 'echo "exists=true" >> "$GITHUB_OUTPUT"' in workflow
    assert 'echo "exists=false" >> "$GITHUB_OUTPUT"' in workflow
    assert 'test "$failure_code" = ImageNotFound' in workflow
    assert "if: steps.candidate.outputs.exists != 'true'" in workflow
    assert "Reusing immutable candidate" in workflow
    assert "--label \"org.opencontainers.image.revision=$GITHUB_SHA\"" in workflow
    assert ":test" not in workflow
    assert ":prod" not in workflow
    assert ":latest" not in workflow


def test_cn_test_workflow_hands_off_to_reviewed_devops_action() -> None:
    workflow = text()
    assert "client-id: ${{ vars.DEVOPS_PR_APP_CLIENT_ID }}" in workflow
    assert "private-key: ${{ secrets.DEVOPS_PR_APP_PRIVATE_KEY }}" in workflow
    assert "repository: readai-team/aibrary-devops" in workflow
    assert "ref: main" in workflow
    assert "permission-contents: read" in workflow
    assert "persist-credentials: false" in workflow
    assert "uses: ./.devops/.github/actions/deploy-test-cn" in workflow
    assert "product: open-newapi" in workflow
    assert "source-sha: ${{ github.sha }}" in workflow
    assert "source-directory: ." in workflow


def test_existing_manual_publisher_is_us_only() -> None:
    legacy = (REPO_ROOT / ".github/workflows/deploy-test.yml").read_text(encoding="utf-8")
    assert "\n  workflow_dispatch:\n" in legacy
    assert "ECR_US_REPOSITORY: open/open-newapi" in legacy
    assert "ECR_CN_REPOSITORY" not in legacy
    assert "Push to CN ECR" not in legacy
    assert "dkr.ecr.cn-northwest-1.amazonaws.com.cn" not in legacy
    dispatch = legacy.split("workflow_dispatch:", 1)[1].split("env:", 1)[0]
    assert "default: us" in dispatch
    assert "          - us\n" in dispatch
    assert "          - cn\n" not in dispatch
    assert "          - both\n" not in dispatch
