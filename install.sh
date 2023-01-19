# Install kind
if ! command -v kind &> /dev/null
then
    echo "kind could not be found"
    echo "installing kind with brew"
    brew install kind
fi

# Install minecraft-test cluster if it does not exist
if ! kind get clusters | grep -q "minecraft-test"
then
    echo "minecraft-test cluster does not exist"
    echo "creating minecraft-test cluster"
    kind create cluster --name minecraft-test --config kind.yaml
fi

