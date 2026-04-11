# Test Python processor for git-sync e2e validation
# This file verifies that git-sync correctly syncs custom components from a Git repository

def test_function():
    """Simple test function to verify file was synced"""
    return "Git-sync test processor loaded successfully!"

# Minimal processor structure for validation
class TestProcessor:
    version = "1.0.0-test"
    description = "Test processor to validate git-sync functionality"
    
    def __init__(self):
        self.name = "GitSyncTestProcessor"
    
    def process(self, input_data):
        return f"Processed by {self.name}: {input_data}"

if __name__ == "__main__":
    processor = TestProcessor()
    print(test_function())
    print(processor.process("test data"))
