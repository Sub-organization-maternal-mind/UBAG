// swift-tools-version:5.7
import PackageDescription

let package = Package(
    name: "Ubag",
    products: [
        .library(name: "Ubag", targets: ["Ubag"]),
    ],
    targets: [
        .target(name: "Ubag"),
        .testTarget(name: "UbagTests", dependencies: ["Ubag"]),
    ]
)
