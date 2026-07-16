.PHONY: validate

validate:
	python3 -m unittest discover -s tests -p 'test_*.py'
	python3 scripts/validate_repository.py
