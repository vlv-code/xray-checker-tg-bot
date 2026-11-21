## How to contribute to Xray Checker

#### **Did you find a bug?**

- **Ensure the bug was not already reported** by searching on GitHub under [Issues](https://github.com/vlv-code/xray-checker-tg-bot/issues).

- If you're unable to find an open issue addressing the problem, [open a new one](https://github.com/vlv-code/xray-checker-tg-bot/issues/new). Be sure to include a **title and clear description**, as much relevant information as possible, and a **code sample** or an **executable test case** demonstrating the expected behavior that is not occurring.

#### **Did you write a patch that fixes a bug?**

- Open a new GitHub pull request with the patch.

- Ensure the PR description clearly describes the problem and solution. Include the relevant issue number if applicable.

#### **Do you intend to add a new feature or change an existing one?**

- Suggest your change in [GitHub Discussions / Issues](https://github.com/vlv-code/xray-checker-tg-bot/discussions) and start writing code.

- Do not open an issue on GitHub until you have collected positive feedback about the change. GitHub issues are primarily intended for bug reports and fixes.

#### **Running the race detector locally**

CI runs `go test -race ./...` on Linux. On Windows the race runtime needs a C toolchain (CGO), so a bare Go installation cannot link it and the command fails. Two supported local setups:

- **WSL2 (recommended):** install gcc inside the distro (`sudo apt update && sudo apt install -y gcc`), then from the repository checkout run `CGO_ENABLED=1 go test -race ./...`.

- **Windows-native (MSYS2):** install the mingw-w64 toolchain with `pacman -S mingw-w64-ucrt-x86_64-gcc`, add the MSYS2 `ucrt64\bin` directory to PATH, then run `CGO_ENABLED=1 go test -race ./...`. Note: clang alone cannot link the Windows race runtime — a full mingw-w64 gcc environment is required.

Thanks! ❤️ ❤️ ❤️
