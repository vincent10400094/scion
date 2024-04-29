load("@bazel_gazelle//:deps.bzl", "go_repository")

def go_dependencies():
    go_repository(
        name = "com_github_andreburgaud_crypt2go",
        importpath = "github.com/andreburgaud/crypt2go",
        sum = "h1:7hz8l9WjaMEtAUL4+nMm64Of7HzUr1H4JhmNof7BCLc=",
        version = "v1.5.0",
    )
    go_repository(
        name = "com_github_klauspost_cpuid_v2",
        importpath = "github.com/klauspost/cpuid/v2",
        sum = "h1:ndNyv040zDGIDh8thGkXYjnFtiN02M1PVVF+JE/48xc=",
        version = "v2.2.6",
    )
    go_repository(
        name = "com_github_klauspost_reedsolomon",
        importpath = "github.com/klauspost/reedsolomon",
        sum = "h1:NhWgum1efX1x58daOBGCFWcxtEhOhXKKl1HAPQUp03Q=",
        version = "v1.12.1",
    )
