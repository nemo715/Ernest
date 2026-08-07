from qiskit import QuantumCircuit
from qiskit_aer import AerSimulator
from qiskit_aer.noise import NoiseModel, depolarizing_error, ReadoutError
from qiskit.quantum_info import hellinger_fidelity
import json
import numpy as np

# Initialize the Quantum Circuit
qc = QuantumCircuit(2)
qc.h(0)  # Hadamard gate on qubit 0
qc.cx(0, 1)  # CNOT gate from qubit 0 to qubit 1
qc.measure_all()  # Measure all qubits

# Create the noise model
nm = NoiseModel()
nm.add_all_qubit_quantum_error(depolarizing_error(0.02, 1), ['u1', 'u2', 'u3'])
nm.add_all_qubit_quantum_error(depolarizing_error(0.04, 2), ['cx'])
nm.add_all_qubit_readout_error(ReadoutError([[0.95, 0.05], [0.03, 0.97]]))

# Simulate with noise
sim = AerSimulator(noise_model=nm)
result = sim.run(qc, shots=4096).result()
noisy_counts = result.get_counts()

# Ideal simulation without noise
ideal_sim = AerSimulator()
ideal_result = ideal_sim.run(qc, shots=4096).result()
ideal_counts = ideal_result.get_counts()

# Normalize counts to probability distributions
ideal_probs = {k: v / 4096 for k, v in ideal_counts.items()}
noisy_probs = {k: v / 4096 for k, v in noisy_counts.items()}

# Calculate Hellinger fidelity
fidelity = hellinger_fidelity(ideal_probs, noisy_probs)
error_rate = 1 - fidelity

# Prepare the output dictionary
output = {
    "ideal_counts": ideal_counts,
    "noisy_counts": noisy_counts,
    "fidelity": fidelity,
    "error_rate": error_rate,
}